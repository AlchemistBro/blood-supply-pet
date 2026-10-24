package c03_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/adapters/c03"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/domain"
)

// Здесь C03 — простая подделка на httptest: проверяется перевод ответов в исходы, а не поведение перевозчика.
// Настоящий имитатор участвует в интеграционных тестах app.

func order() app.Order {
	return app.Order{ClientRef: "o07-x", Request: domain.Request{
		ObligationID: uuid.New(), RequestID: uuid.New(), SourceClinicID: 1, TargetClinicID: 2,
		ComponentKindCode: "RBC", Quantity: 2, TemperatureRegime: "COLD_2_6",
	}}
}

func serve(t *testing.T, h http.HandlerFunc) (*c03.Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return c03.New(srv.URL, 5*time.Second), &calls
}

func problem(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"title": "t", "status": status, "detail": detail})
}

func TestSubmitSendsOrderAndReturnsShipmentRef(t *testing.T) {
	var got map[string]any
	var idempotencyKey string
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey = r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"shipmentRef":"C03-5","clientRef":"o07-x"}`))
	})
	res, err := c.Submit(context.Background(), order())
	if err != nil || res.Refused || res.ShipmentRef != "C03-5" {
		t.Fatalf("отправка: %+v, %v", res, err)
	}
	if got["clientRef"] != "o07-x" || got["containers"] != float64(2) || got["temperatureRegime"] != "COLD_2_6" ||
		got["componentKind"] != "RBC" || got["sourceClinicId"] != float64(1) || got["targetClinicId"] != float64(2) {
		t.Fatalf("тело заказа: %+v", got)
	}
	// С Idempotency-Key net/http сам повторил бы POST после обрыва и создал второй заказ.
	if idempotencyKey != "" {
		t.Fatal("POST не должен нести Idempotency-Key")
	}
}

func TestRefusalAndInvalidOrderAreRefused(t *testing.T) {
	for status, detail := range map[int]string{
		http.StatusUnprocessableEntity: "нет свободной машины",
		http.StatusBadRequest:          "число контейнеров вне диапазона 1..50",
	} {
		c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) { problem(w, status, detail) })
		res, err := c.Submit(context.Background(), order())
		if err != nil || !res.Refused || res.Reason == "" {
			t.Fatalf("%d: %+v, %v", status, res, err)
		}
	}
}

func TestUnavailableCarrierIsUnknownOutcomeWithoutRetries(t *testing.T) {
	c, calls := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		problem(w, http.StatusServiceUnavailable, "C03 не отвечает")
	})
	if _, err := c.FindActive(context.Background(), "o07-x"); !errors.Is(err, app.ErrCarrierUnavailable) {
		t.Fatalf("сверка при 503: %v", err)
	}
	if _, err := c.Submit(context.Background(), order()); !errors.Is(err, app.ErrCarrierUnavailable) {
		t.Fatalf("отправка при 503: %v", err)
	}
	if err := c.Cancel(context.Background(), "o07-x"); !errors.Is(err, app.ErrCarrierUnavailable) {
		t.Fatalf("отмена при 503: %v", err)
	}
	// Повторы съели бы попытки шлюза: один вызов — одна попытка.
	if n := calls.Load(); n != 3 {
		t.Fatalf("вызовов %d, ожидали 3 без повторов", n)
	}
}

// Предохранитель не глушит C03: после длинной серии 503 клиент всё ещё ходит к перевозчику.
func TestManyFailuresDoNotOpenCircuit(t *testing.T) {
	var down atomic.Bool
	down.Store(true)
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		if down.Load() {
			problem(w, http.StatusServiceUnavailable, "C03 не отвечает")
			return
		}
		_, _ = w.Write([]byte(`{"shipments":[]}`))
	})
	for range 20 {
		_, _ = c.FindActive(context.Background(), "o07-x")
	}
	down.Store(false)
	if ref, err := c.FindActive(context.Background(), "o07-x"); err != nil || ref != "" {
		t.Fatalf("после восстановления C03 сверка: %q, %v", ref, err)
	}
}

func TestDroppedConnectionIsUnknownOutcome(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	})
	if _, err := c.Submit(context.Background(), order()); !errors.Is(err, app.ErrCarrierUnavailable) {
		t.Fatalf("обрыв соединения: %v", err)
	}
}

func TestFindActiveTakesFirstNotCancelled(t *testing.T) {
	var query string
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query().Get("clientRef")
		_, _ = w.Write([]byte(`{"shipments":[{"shipmentRef":"C03-1","cancelled":true},{"shipmentRef":"C03-2"},{"shipmentRef":"C03-3"}]}`))
	})
	ref, err := c.FindActive(context.Background(), "o07-x")
	if err != nil || ref != "C03-2" {
		t.Fatalf("сверка: %q, %v", ref, err)
	}
	if query != "o07-x" {
		t.Fatalf("clientRef в запросе %q", query)
	}
}

func TestCancelDeletesByClientRef(t *testing.T) {
	var method, query string
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		method, query = r.Method, r.URL.Query().Get("clientRef")
		_, _ = w.Write([]byte(`{"shipments":[]}`))
	})
	if err := c.Cancel(context.Background(), "o07-x"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete || query != "o07-x" {
		t.Fatalf("отмена: %s ?clientRef=%s", method, query)
	}
}
