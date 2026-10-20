package httpx

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/correlation"
)

// HeaderCorrelationID — заголовок сквозного идентификатора запроса.
const HeaderCorrelationID = "X-Correlation-Id"

// Middleware — обёртка над обработчиком.
type Middleware func(http.Handler) http.Handler

// Chain применяет middleware слева направо: первый — самый внешний.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// CorrelationID берёт идентификатор из заголовка или создаёт новый, кладёт в контекст и ответ.
func CorrelationID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(HeaderCorrelationID)
			if id == "" || len(id) > 64 {
				id = correlation.NewID()
			}
			ctx := correlation.With(r.Context(), correlation.IDs{Correlation: id})
			w.Header().Set(HeaderCorrelationID, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Recover превращает panic в 500 problem+json и пишет стек в лог.
func Recover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if p := recover(); p != nil {
					log.ErrorContext(r.Context(), "panic в обработчике", "panic", p, "stack", string(debug.Stack()),
						"correlation_id", correlation.From(r.Context()).Correlation)
					WriteProblem(w, r, http.StatusInternalServerError, "Внутренняя ошибка", "Запрос не выполнен. Повторите позже.")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// AccessLog пишет одну строку на запрос: метод, путь, статус, длительность, корреляция.
// Путей с ПДн в системе нет; тела запросов не логируются.
func AccessLog(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/metrics" {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			log.InfoContext(r.Context(), "http",
				"method", r.Method, "path", r.URL.Path, "status", sw.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"correlation_id", correlation.From(r.Context()).Correlation)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Flush поддерживает SSE через обёртку.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Standard — стандартный набор middleware сервиса.
func Standard(log *slog.Logger) []Middleware {
	return []Middleware{CorrelationID(), Recover(log), AccessLog(log)}
}
