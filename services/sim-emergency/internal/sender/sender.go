// Package sender отправляет прогноз ЧС в planning по ИНТ-02: POST {BS_PLANNING_URL}/int/forecast
// со служебным токеном (Bearer). Получение токена скрыто за узким интерфейсом TokenSource: реализация
// client credentials подключается отдельно, для локальной разработки есть NoAuth (BS_SIM_NO_AUTH).
package sender

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
)

// ForecastPath — путь приёма прогноза в planning (ИНТ-02).
const ForecastPath = "/int/forecast"

// maxResponseBytes — сколько ответа planning (квитанции) сохранять для GET /dev/forecasts.
const maxResponseBytes = 4 << 10

// TokenSource выдаёт служебный токен для вызова planning. Пустая строка без ошибки — запрос без заголовка.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// ErrNoServiceToken — получение служебного токена не настроено.
var ErrNoServiceToken = errors.New("служебный токен не настроен: client credentials не подключены, для локальной разработки задайте BS_SIM_NO_AUTH=true")

// NoAuth — без токена: только для локальной разработки, когда planning запущен с отключённой проверкой JWT.
type NoAuth struct{}

// Token возвращает пустой токен.
func (NoAuth) Token(context.Context) (string, error) { return "", nil }

// Unconfigured — токен не настроен: каждая отправка завершается ErrNoServiceToken, а не уходит без токена.
type Unconfigured struct{}

// Token всегда возвращает ErrNoServiceToken.
func (Unconfigured) Token(context.Context) (string, error) { return "", ErrNoServiceToken }

// Result — ответ planning.
type Result struct {
	Status int
	Body   string
}

// Sender — отправитель прогноза.
type Sender struct {
	url    string
	tokens TokenSource
	client *httpx.Client
}

// New создаёт отправителя. planningURL — база planning (или nginx с префиксом /api), без /int/forecast.
func New(planningURL string, tokens TokenSource, timeout time.Duration) *Sender {
	return &Sender{
		url:    strings.TrimRight(planningURL, "/") + ForecastPath,
		tokens: tokens,
		// POST не повторяется: повтор с той же scenario_version planning отклонит, исход виден в ответе.
		client: httpx.NewClient(httpx.ClientConfig{Name: "planning ИНТ-02", Timeout: timeout}),
	}
}

// URL — адрес приёма прогноза.
func (s *Sender) URL() string { return s.url }

// Send отправляет тело прогноза. Ответ planning любого кода 2xx–4xx возвращается как Result без ошибки:
// отказ в приёме (например, повтор версии) — результат, который имитатор показывает, а не сбой.
func (s *Sender) Send(ctx context.Context, body []byte) (Result, error) {
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("служебный токен: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("запрос в planning: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("отправка прогноза в planning: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return Result{Status: resp.StatusCode}, fmt.Errorf("чтение ответа planning: %w", err)
	}
	return Result{Status: resp.StatusCode, Body: string(data)}, nil
}
