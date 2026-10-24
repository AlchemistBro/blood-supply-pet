// Package inventoryclient — единственный синхронный вызов между сервисами: transfer → inventory,
// GET /internal/units/status (ADR-0004). Таймаут 3 с, предохранитель, служебный токен (роль SERVICE).
// Любой сбой — «статус не получен» (app.ErrSourceSystemUnavailable): подтверждать вслепую нельзя.
// Типы ответа описаны здесь по контракту services/inventory/api/openapi.yaml (UnitStatusReport):
// импорт внутренних пакетов inventory запрещён (ADR-0005).
package inventoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

// MaxBatch — сколько unit_id принимает один запрос inventory (контракт: 1–1000).
const MaxBatch = 1000

// Источники ответа inventory.
const (
	sourceAIST = "aist"
	sourceCopy = "copy"
)

// Config — адрес inventory и параметры вызова (переменные окружения BS_INVENTORY_*).
type Config struct {
	BaseURL         string        `env:"BS_INVENTORY_URL" envDefault:"http://inventory:8080"`
	Timeout         time.Duration `env:"BS_INVENTORY_TIMEOUT" envDefault:"3s"`
	BreakerFailures uint32        `env:"BS_INVENTORY_BREAKER_FAILURES" envDefault:"5"`
	BreakerOpen     time.Duration `env:"BS_INVENTORY_BREAKER_OPEN" envDefault:"10s"`
}

// Authorizer ставит служебный токен в запрос (auth.ServiceToken).
type Authorizer interface {
	Authorize(req *http.Request) error
}

// Client реализует app.UnitStatusChecker.
type Client struct {
	http    *httpx.Client
	base    string
	timeout time.Duration
	authz   Authorizer
}

var _ app.UnitStatusChecker = (*Client)(nil)

// New создаёт клиента. Сетевых запросов не делает.
func New(cfg Config, authz Authorizer) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("клиент inventory: не задан BS_INVENTORY_URL")
	}
	if authz == nil {
		return nil, errors.New("клиент inventory: нужен источник служебного токена")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 3 * time.Second
	}
	return &Client{
		// Повторов нет: весь вызов укладывается в 3 с, оператор ждёт ответа в том же запросе.
		http: httpx.NewClient(httpx.ClientConfig{
			Name: "inventory", Timeout: cfg.Timeout, BreakerFailures: cfg.BreakerFailures, BreakerOpen: cfg.BreakerOpen,
		}),
		base: strings.TrimRight(cfg.BaseURL, "/"), timeout: cfg.Timeout, authz: authz,
	}, nil
}

type unitStatus struct {
	UnitID    uuid.UUID `json:"unit_id"`
	Status    string    `json:"status"`
	Available bool      `json:"available"`
}

type unitStatusReport struct {
	Source    string       `json:"source"`
	Watermark *time.Time   `json:"watermark"`
	Units     []unitStatus `json:"units"`
	NotFound  []uuid.UUID  `json:"not_found"`
}

// CheckUnits спрашивает статус единиц пачками до MaxBatch. Единица без ответа, неизвестный статус или
// available=false — недоступна. Хотя бы одна пачка из копии — весь ответ из копии.
func (c *Client) CheckUnits(ctx context.Context, ids []uuid.UUID) (app.UnitStatusReport, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	out := app.UnitStatusReport{Statuses: make(map[uuid.UUID]domain.UnitStatus, len(ids))}
	for start := 0; start < len(ids); start += MaxBatch {
		end := min(start+MaxBatch, len(ids))
		rep, err := c.batch(ctx, ids[start:end])
		if err != nil {
			return app.UnitStatusReport{}, fmt.Errorf("%w: %w", app.ErrSourceSystemUnavailable, err)
		}
		if rep.Source == sourceCopy {
			out.FromCopy = true
		}
		for _, u := range rep.Units {
			st, err := domain.ParseUnitStatus(u.Status)
			if err != nil {
				continue // неизвестный статус — недоступна
			}
			if !u.Available && st.Allocatable() {
				continue // справочник inventory считает единицу недоступной: в ответ не берём, не закрепляем
			}
			out.Statuses[u.UnitID] = st
		}
	}
	return out, nil
}

func (c *Client) batch(ctx context.Context, ids []uuid.UUID) (unitStatusReport, error) {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = id.String()
	}
	u := c.base + "/internal/units/status?ids=" + url.QueryEscape(strings.Join(parts, ","))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return unitStatusReport{}, fmt.Errorf("запрос статуса единиц: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if err := c.authz.Authorize(req); err != nil {
		return unitStatusReport{}, fmt.Errorf("служебный токен: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// url.Error включает полный URL с unit_id: наружу отдаётся только вид отказа.
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return unitStatusReport{}, fmt.Errorf("статус единиц: %w", context.DeadlineExceeded)
		case errors.Is(err, context.Canceled):
			return unitStatusReport{}, fmt.Errorf("статус единиц: %w", context.Canceled)
		case errors.Is(err, httpx.ErrCircuitOpen):
			return unitStatusReport{}, fmt.Errorf("статус единиц: %w", httpx.ErrCircuitOpen)
		default:
			return unitStatusReport{}, errors.New("сетевая ошибка получения статуса единиц")
		}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			slog.WarnContext(ctx, "Отказ служебному токену transfer при обращении к inventory", "status", resp.StatusCode)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return unitStatusReport{}, fmt.Errorf("inventory ответил %d", resp.StatusCode)
	}
	var rep unitStatusReport
	if err := json.NewDecoder(resp.Body).Decode(&rep); err != nil {
		return unitStatusReport{}, fmt.Errorf("разбор ответа inventory: %w", err)
	}
	if rep.Source != sourceAIST && rep.Source != sourceCopy {
		return unitStatusReport{}, fmt.Errorf("неизвестный source %q в ответе inventory", rep.Source)
	}
	return rep, nil
}
