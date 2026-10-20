// Package observability — логи, трассы и метрики одного стандарта для всех сервисов:
// slog в JSON с маскированием чувствительных полей, OpenTelemetry (OTLP) для трасс и метрик,
// Prometheus-эндпоинт /metrics. correlation_id проходит через контекст в логи.
package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/AlchemistBro/blood-supply-pet/libs/correlation"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
)

// Config — настройки наблюдаемости.
type Config struct {
	ServiceName string
	Version     string
	Environment string
	// OTLPEndpoint — адрес коллектора (host:port) для трасс и метрик; пусто — только /metrics и логи.
	OTLPEndpoint string
	LogLevel     string
	// LogFormat — json (по умолчанию) или text для локальной разработки.
	LogFormat string
}

// Observability — собранные провайдеры.
type Observability struct {
	Logger         *slog.Logger
	MetricsHandler http.Handler
	Tracer         trace.Tracer
	shutdown       []func(context.Context) error
}

// sensitiveKeys — атрибуты, которые никогда не попадают в логи (NFR010, NFR016, ADR-0011).
var sensitiveKeys = map[string]bool{
	"patient_case_no": true, "phenotype": true, "snils": true, "full_name": true, "password": true,
	"authorization": true, "token": true, "email": true,
}

// Init настраивает логи, трассы и метрики. Shutdown обязателен при остановке.
func Init(ctx context.Context, cfg Config) (*Observability, error) {
	level := parseLevel(cfg.LogLevel)
	var base slog.Handler
	opts := &slog.HandlerOptions{Level: level, ReplaceAttr: maskAttr}
	if strings.EqualFold(cfg.LogFormat, "text") {
		base = slog.NewTextHandler(os.Stdout, opts)
	} else {
		base = slog.NewJSONHandler(os.Stdout, opts)
	}
	logger := slog.New(&contextHandler{Handler: base}).With("service", cfg.ServiceName, "version", cfg.Version)
	slog.SetDefault(logger)

	// Атрибуты без schema URL: иначе версия semconv SDK конфликтует с нашей.
	res, err := resource.New(ctx, resource.WithTelemetrySDK(), resource.WithAttributes(
		semconv.ServiceName(cfg.ServiceName), semconv.ServiceVersion(cfg.Version),
		semconv.DeploymentEnvironment(cfg.Environment)))
	if err != nil {
		return nil, fmt.Errorf("ресурс OTel: %w", err)
	}
	o := &Observability{Logger: logger}

	// Метрики: Prometheus всегда, OTLP при наличии коллектора.
	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	promExporter, err := otelprom.New(otelprom.WithRegisterer(registry))
	if err != nil {
		return nil, fmt.Errorf("экспортёр Prometheus: %w", err)
	}
	metricOpts := []sdkmetric.Option{sdkmetric.WithResource(res), sdkmetric.WithReader(promExporter)}
	if cfg.OTLPEndpoint != "" {
		mexp, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpoint(cfg.OTLPEndpoint), otlpmetrichttp.WithInsecure())
		if err != nil {
			return nil, fmt.Errorf("экспортёр метрик OTLP: %w", err)
		}
		metricOpts = append(metricOpts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(mexp, sdkmetric.WithInterval(15*time.Second))))
	}
	mp := sdkmetric.NewMeterProvider(metricOpts...)
	otel.SetMeterProvider(mp)
	o.shutdown = append(o.shutdown, mp.Shutdown)
	o.MetricsHandler = promhttp.HandlerFor(registry, promhttp.HandlerOpts{})

	// Трассы: OTLP при наличии коллектора, иначе провайдер без экспорта (контексты всё равно пробрасываются).
	traceOpts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if cfg.OTLPEndpoint != "" {
		texp, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpoint(cfg.OTLPEndpoint), otlptracehttp.WithInsecure())
		if err != nil {
			return nil, fmt.Errorf("экспортёр трасс OTLP: %w", err)
		}
		traceOpts = append(traceOpts, sdktrace.WithBatcher(texp))
	}
	tp := sdktrace.NewTracerProvider(traceOpts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	o.shutdown = append(o.shutdown, tp.Shutdown)
	o.Tracer = tp.Tracer(cfg.ServiceName)
	return o, nil
}

// Shutdown сбрасывает буферы экспортёров.
func (o *Observability) Shutdown(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var errs []error
	for i := len(o.shutdown) - 1; i >= 0; i-- {
		if err := o.shutdown[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// HTTPMiddleware оборачивает обработчик трассировкой otelhttp.
func HTTPMiddleware(serviceName string) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, serviceName, otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			if r.Pattern != "" {
				return r.Method + " " + r.Pattern
			}
			return r.Method + " " + r.URL.Path
		}))
	}
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// maskAttr скрывает значения чувствительных ключей на любом уровне вложенности.
func maskAttr(_ []string, a slog.Attr) slog.Attr {
	if sensitiveKeys[strings.ToLower(a.Key)] {
		return slog.String(a.Key, "[скрыто]")
	}
	return a
}

// contextHandler добавляет correlation_id и trace_id из контекста к каждой записи.
type contextHandler struct{ slog.Handler }

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if ids := correlation.From(ctx); ids.Correlation != "" && !hasAttr(r, "correlation_id") {
		r.AddAttrs(slog.String("correlation_id", ids.Correlation))
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithGroup(name)}
}

func hasAttr(r slog.Record, key string) bool {
	found := false
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			found = true
			return false
		}
		return true
	})
	return found
}
