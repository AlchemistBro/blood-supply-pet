// Package c03 — клиент перевозчика C03 (ИНТ-03) поверх libs/httpx. Реализует app.Carrier.
//
// Реальный API C03 неизвестен, поэтому контракт пессимистичный, как в прототипе: отправка не идемпотентна,
// ответ может потеряться, но заказ находится по нашему clientRef. Отсюда правила клиента:
//   - повторов нет (Retries: 0): повтором заказа управляет шлюз через сверку, а повтор GET внутри клиента
//     съел бы несколько попыток шлюза за одну;
//   - предохранитель фактически выключен: после серии 503 разомкнутая цепь не пустила бы и сверку,
//     а без сверки шлюз не узнает, дошёл ли заказ;
//   - заголовка Idempotency-Key на POST нет: с ним net/http сам повторяет POST после обрыва соединения
//     и создаёт второй заказ;
//   - обрыв соединения, таймаут и 5xx — исход неизвестен (app.ErrCarrierUnavailable), решает сверка;
//     422 — отказ перевозчика; 400 на отправку — тоже отказ: такой заказ C03 не примет и при повторе.
package c03

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/app"
)

// maxBody — предел чтения ответа C03.
const maxBody = 1 << 20

// Client — клиент C03.
type Client struct {
	base string
	http *httpx.Client
}

// New создаёт клиента. timeout — на один вызов; три вызова одного drive должны укладываться в аренду строки.
func New(baseURL string, timeout time.Duration) *Client {
	return &Client{
		base: strings.TrimRight(baseURL, "/"),
		http: httpx.NewClient(httpx.ClientConfig{
			Name:    "c03",
			Timeout: timeout,
			Retries: 0,
			// Порог, который не достигается: предохранитель не должен глушить сверку (см. комментарий пакета).
			BreakerFailures: math.MaxUint32,
		}),
	}
}

type order struct {
	ClientRef         string `json:"clientRef"`
	SourceClinicID    int64  `json:"sourceClinicId"`
	TargetClinicID    int64  `json:"targetClinicId"`
	ComponentKind     string `json:"componentKind"`
	Containers        int    `json:"containers"`
	TemperatureRegime string `json:"temperatureRegime"`
}

type shipment struct {
	ShipmentRef string `json:"shipmentRef"`
	Cancelled   bool   `json:"cancelled"`
}

type shipmentList struct {
	Shipments []shipment `json:"shipments"`
}

type problem struct {
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

// FindActive — первый активный заказ по clientRef; пусто — заказа нет.
func (c *Client) FindActive(ctx context.Context, clientRef string) (string, error) {
	var list shipmentList
	status, err := c.call(ctx, http.MethodGet, "/shipments?clientRef="+url.QueryEscape(clientRef), nil, &list)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", unavailable("сверка: C03 ответила %d", status)
	}
	for _, s := range list.Shipments {
		if !s.Cancelled && s.ShipmentRef != "" {
			return s.ShipmentRef, nil
		}
	}
	return "", nil
}

// Submit отправляет заказ. Число контейнеров — quantity команды; заказ не делится: один заказ на обязательство
// держит сверку по clientRef. Превышение предела C03 приходит как 400 и становится отказом с причиной от C03.
func (c *Client) Submit(ctx context.Context, o app.Order) (app.SubmitResult, error) {
	body := order{
		ClientRef: o.ClientRef, SourceClinicID: o.SourceClinicID, TargetClinicID: o.TargetClinicID,
		ComponentKind: o.ComponentKindCode, Containers: o.Quantity, TemperatureRegime: o.TemperatureRegime,
	}
	var created shipment
	var refused problem
	status, err := c.call(ctx, http.MethodPost, "/shipments", body, &created, withProblem(&refused))
	if err != nil {
		return app.SubmitResult{}, err
	}
	switch {
	case status == http.StatusCreated && created.ShipmentRef != "":
		return app.SubmitResult{ShipmentRef: created.ShipmentRef}, nil
	case status == http.StatusUnprocessableEntity:
		return app.SubmitResult{Refused: true, Reason: reason(refused, "перевозчик отказал")}, nil
	case status == http.StatusBadRequest:
		return app.SubmitResult{Refused: true,
			Reason: "C03 отклонила заказ как некорректный: " + reason(refused, "без пояснения")}, nil
	default:
		// 201 без номера или неожиданный ответ: заказ мог создаться, решит сверка.
		return app.SubmitResult{}, unavailable("отправка: C03 ответила %d", status)
	}
}

// Cancel снимает все заказы clientRef; отсутствие заказов не ошибка.
func (c *Client) Cancel(ctx context.Context, clientRef string) error {
	status, err := c.call(ctx, http.MethodDelete, "/shipments?clientRef="+url.QueryEscape(clientRef), nil, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return unavailable("отмена: C03 ответила %d", status)
	}
	return nil
}

type callOpt func(*callOpts)

type callOpts struct{ problem *problem }

func withProblem(p *problem) callOpt { return func(o *callOpts) { o.problem = p } }

// call выполняет запрос и разбирает 2xx в out, 4xx — в problem, если он задан. Возвращает код ответа.
// Сетевые ошибки, таймаут и 5xx оборачивают app.ErrCarrierUnavailable; отмена контекста возвращается как есть.
func (c *Client) call(ctx context.Context, method, path string, in, out any, opts ...callOpt) (int, error) {
	var o callOpts
	for _, opt := range opts {
		opt(&o)
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, fmt.Errorf("сериализация запроса к C03: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return 0, fmt.Errorf("запрос к C03: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, fmt.Errorf("вызов C03 прерван: %w", ctx.Err())
		}
		return 0, fmt.Errorf("%w: %s %s: %w", app.ErrCarrierUnavailable, method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		// Ответ оборвался посреди тела: исход неизвестен.
		return 0, fmt.Errorf("%w: чтение ответа %s %s: %w", app.ErrCarrierUnavailable, method, path, err)
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300 && out != nil && len(raw) > 0:
		if err := json.Unmarshal(raw, out); err != nil {
			return 0, fmt.Errorf("%w: разбор ответа %s %s: %w", app.ErrCarrierUnavailable, method, path, err)
		}
	case resp.StatusCode >= 400 && o.problem != nil && len(raw) > 0:
		// Тело problem+json необязательно: без него остаётся код ответа.
		_ = json.Unmarshal(raw, o.problem)
	}
	return resp.StatusCode, nil
}

func unavailable(format string, args ...any) error {
	return fmt.Errorf("%w: %s", app.ErrCarrierUnavailable, fmt.Sprintf(format, args...))
}

func reason(p problem, fallback string) string {
	switch {
	case p.Detail != "":
		return p.Detail
	case p.Title != "":
		return p.Title
	default:
		return fallback
	}
}

// Проверка на этапе компиляции.
var _ app.Carrier = (*Client)(nil)
