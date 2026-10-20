package httpx

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Check — проверка готовности зависимости (БД, Kafka, миграции).
type Check func(ctx context.Context) error

// Health регистрирует проверки и отдаёт /healthz и /readyz.
// /healthz отвечает 200, пока процесс жив; /readyz — только когда все зависимости доступны.
type Health struct {
	mu     sync.RWMutex
	checks map[string]Check
}

// NewHealth создаёт реестр проверок.
func NewHealth() *Health { return &Health{checks: map[string]Check{}} }

// AddReadiness регистрирует проверку под именем.
func (h *Health) AddReadiness(name string, c Check) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks[name] = c
}

// Register добавляет маршруты в mux.
func (h *Health) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /readyz", h.ready)
}

func (h *Health) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	h.mu.RLock()
	checks := make(map[string]Check, len(h.checks))
	for k, v := range h.checks {
		checks[k] = v
	}
	h.mu.RUnlock()

	type result struct {
		Name string `json:"name"`
		OK   bool   `json:"ok"`
		Err  string `json:"error,omitempty"`
	}
	results := make([]result, 0, len(checks))
	allOK := true
	var wg sync.WaitGroup
	var mu sync.Mutex
	for name, c := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := c(ctx)
			mu.Lock()
			defer mu.Unlock()
			res := result{Name: name, OK: err == nil}
			if err != nil {
				res.Err = err.Error()
				allOK = false
			}
			results = append(results, res)
		}()
	}
	wg.Wait()
	w.Header().Set("Content-Type", "application/json")
	if !allOK {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ready": allOK, "checks": results})
}
