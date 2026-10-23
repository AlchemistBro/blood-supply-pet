package httpx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/sony/gobreaker/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/AlchemistBro/blood-supply-pet/libs/correlation"
)

// ClientConfig — таймаут, повторы и предохранитель для вызовов смежных систем и сервисов.
type ClientConfig struct {
	Name    string
	Timeout time.Duration
	// Retries — число повторов для идемпотентных запросов (GET, PUT с ключом).
	Retries int
	// BreakerFailures — после скольких подряд ошибок размыкать цепь.
	BreakerFailures uint32
	// BreakerOpen — сколько держать цепь разомкнутой.
	BreakerOpen time.Duration
	// Meter — откуда брать счётчик исходов; по умолчанию глобальный провайдер OTel, который
	// libs/observability выводит в /metrics (Prometheus).
	Meter metric.Meter
}

// ClientCallsMetric — счётчик вызовов клиента с атрибутами client (имя клиента) и outcome
// (ok, error, timeout, circuit_open). В Prometheus — httpx_client_calls_total.
const ClientCallsMetric = "httpx.client.calls"

// Исходы вызова для метрики. ok — ответ получен (в том числе 4xx: это ответ, а не отказ), error — сетевая
// ошибка или 5xx после повторов, timeout — истёк таймаут, circuit_open — предохранитель не пустил вызов.
const (
	OutcomeOK          = "ok"
	OutcomeError       = "error"
	OutcomeTimeout     = "timeout"
	OutcomeCircuitOpen = "circuit_open"
)

// Client — HTTP-клиент с корреляцией, повторами и предохранителем (ADR-0004: таймаут 3 с для статуса единиц).
type Client struct {
	http    *http.Client
	cfg     ClientConfig
	breaker *gobreaker.CircuitBreaker[*http.Response]
	calls   metric.Int64Counter
}

// ErrCircuitOpen — предохранитель разомкнут, вызов не делался.
var ErrCircuitOpen = errors.New("предохранитель разомкнут")

// NewClient создаёт клиента.
func NewClient(cfg ClientConfig) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 3 * time.Second
	}
	if cfg.BreakerFailures == 0 {
		cfg.BreakerFailures = 5
	}
	if cfg.BreakerOpen <= 0 {
		cfg.BreakerOpen = 10 * time.Second
	}
	//nolint:bodyclose // тело ответа закрывает вызывающий Client.Do; при отказе 5xx оно закрыто в Do
	cb := gobreaker.NewCircuitBreaker[*http.Response](gobreaker.Settings{
		Name:    cfg.Name,
		Timeout: cfg.BreakerOpen,
		ReadyToTrip: func(c gobreaker.Counts) bool {
			return c.ConsecutiveFailures >= cfg.BreakerFailures
		},
		IsSuccessful: func(err error) bool {
			return err == nil
		},
	})
	if cfg.Meter == nil {
		cfg.Meter = otel.GetMeterProvider().Meter("github.com/AlchemistBro/blood-supply-pet/libs/httpx")
	}
	calls, err := cfg.Meter.Int64Counter(ClientCallsMetric,
		metric.WithDescription("Вызовы HTTP-клиента по исходу: ok, error, timeout, circuit_open"))
	if err != nil {
		// Счётчик без имени и единиц не может не создаться; на всякий случай работаем без метрики.
		calls = noop.Int64Counter{}
	}
	return &Client{
		http:    &http.Client{Timeout: cfg.Timeout, Transport: &correlationTransport{base: http.DefaultTransport}},
		cfg:     cfg,
		breaker: cb,
		calls:   calls,
	}
}

// Do выполняет запрос через предохранитель; 5xx и сетевые ошибки считаются отказами,
// идемпотентные запросы повторяются с экспоненциальной паузой и джиттером.
// Исход вызова (после всех повторов) попадает в счётчик ClientCallsMetric.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.do(req)
	c.calls.Add(req.Context(), 1, metric.WithAttributes(
		attribute.String("client", c.cfg.Name), attribute.String("outcome", outcomeOf(err))))
	return resp, err
}

func outcomeOf(err error) string {
	if err == nil {
		return OutcomeOK
	}
	if errors.Is(err, ErrCircuitOpen) {
		return OutcomeCircuitOpen
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return OutcomeTimeout
	}
	return OutcomeError
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	attempts := 1
	if idempotent(req.Method) && c.cfg.Retries > 0 {
		attempts += c.cfg.Retries
	}
	var lastErr error
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 100 * time.Millisecond
	bo.MaxInterval = time.Second
	for i := 0; i < attempts; i++ {
		resp, err := c.breaker.Execute(func() (*http.Response, error) {
			//nolint:gosec // адрес запроса задаёт конфигурация сервиса, пользовательского ввода здесь нет
			r, err := c.http.Do(req)
			if err != nil {
				return nil, err
			}
			if r.StatusCode >= 500 {
				_ = r.Body.Close()
				return nil, fmt.Errorf("%s ответил %d", c.cfg.Name, r.StatusCode)
			}
			return r, nil
		})
		if err == nil {
			return resp, nil
		}
		if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
			return nil, fmt.Errorf("%s: %w", c.cfg.Name, ErrCircuitOpen)
		}
		lastErr = err
		if i+1 < attempts {
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(bo.NextBackOff()):
			}
		}
	}
	return nil, fmt.Errorf("%s: %w", c.cfg.Name, lastErr)
}

// State возвращает состояние предохранителя (для метрик и /readyz).
func (c *Client) State() string { return c.breaker.State().String() }

func idempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
		return true
	}
	return false
}

type correlationTransport struct{ base http.RoundTripper }

func (t *correlationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if ids := correlation.From(req.Context()); ids.Correlation != "" && req.Header.Get(HeaderCorrelationID) == "" {
		req = req.Clone(req.Context())
		req.Header.Set(HeaderCorrelationID, ids.Correlation)
	}
	return t.base.RoundTrip(req)
}

// WithContextTimeout — удобный помощник для запросов с собственным таймаутом поверх клиента.
func WithContextTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}
