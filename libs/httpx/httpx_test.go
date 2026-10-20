package httpx_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/correlation"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
)

func TestCorrelationAndRecover(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := httpx.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/panic" {
			panic("boom")
		}
		ids := correlation.From(r.Context())
		_, _ = io.WriteString(w, ids.Correlation)
	}), httpx.Standard(log)...)

	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	req.Header.Set(httpx.HeaderCorrelationID, "abc-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.String() != "abc-123" || rec.Header().Get(httpx.HeaderCorrelationID) != "abc-123" {
		t.Fatalf("корреляция не пробросилась: %q %q", rec.Body.String(), rec.Header().Get(httpx.HeaderCorrelationID))
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Header().Get(httpx.HeaderCorrelationID) == "" {
		t.Fatal("новая корреляция не создана")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if rec.Code != http.StatusInternalServerError || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("panic не превращён в problem+json: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Status != 500 || p.CorrelationID == "" {
		t.Fatalf("тело problem: %v %+v", err, p)
	}
}

func TestClientRetriesAndBreaker(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.URL.Path == "/flaky" && n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.URL.Path == "/down" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := httpx.NewClient(httpx.ClientConfig{Name: "test", Retries: 2, BreakerFailures: 3})
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/flaky", nil)
	resp, err := c.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("повтор не помог: %v", err)
	}
	_ = resp.Body.Close()
	if calls.Load() != 2 {
		t.Fatalf("ожидали 2 вызова, было %d", calls.Load())
	}

	c = httpx.NewClient(httpx.ClientConfig{Name: "down", Retries: 0, BreakerFailures: 3})
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/down", nil)
		if resp, err := c.Do(req); err == nil {
			_ = resp.Body.Close()
			t.Fatal("ожидали ошибку 5xx")
		}
	}
	before := calls.Load()
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/down", nil)
	resp, err = c.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	if err == nil || calls.Load() != before {
		t.Fatalf("предохранитель не разомкнулся: err=%v calls=%d", err, calls.Load()-before)
	}
	if c.State() != "open" {
		t.Fatalf("состояние предохранителя %s", c.State())
	}
}

func TestHealthReady(t *testing.T) {
	h := httpx.NewHealth()
	ok := true
	h.AddReadiness("db", func(ctx context.Context) error {
		if ok {
			return nil
		}
		return io.ErrUnexpectedEOF
	})
	mux := http.NewServeMux()
	h.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz при здоровой БД: %d", rec.Code)
	}
	ok = false
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz при больной БД: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz: %d", rec.Code)
	}
}
