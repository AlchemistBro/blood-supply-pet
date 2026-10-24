package httpx_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
)

// Каждый вызов клиента считается по исходу: ok, error, timeout, circuit_open. По этой метрике
// видно, что inventory (или АИСТ-М за ним) отказывает, раньше, чем операторы начнут получать
// «повторите» (ADR-0004, NFR013).
func TestClientCountsOutcomes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fail":
			w.WriteHeader(http.StatusInternalServerError)
		case "/slow":
			time.Sleep(300 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")
	c := httpx.NewClient(httpx.ClientConfig{
		Name: "inventory", Timeout: 50 * time.Millisecond, BreakerFailures: 3, BreakerOpen: time.Minute, Meter: meter,
	})
	call := func(path string) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+path, nil)
		if resp, err := c.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}

	call("/ok")
	call("/fail")
	call("/slow")
	call("/fail") // третий отказ подряд размыкает цепь
	call("/ok")   // цепь разомкнута: вызова нет

	got := collectOutcomes(t, reader, "inventory")
	want := map[string]int64{"ok": 1, "error": 2, "timeout": 1, "circuit_open": 1}
	for outcome, n := range want {
		if got[outcome] != n {
			t.Errorf("исход %s: %d, ожидали %d (все: %v)", outcome, got[outcome], n, got)
		}
	}
}

func collectOutcomes(t *testing.T, reader *sdkmetric.ManualReader, client string) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != httpx.ClientCallsMetric {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("метрика %s не счётчик: %T", m.Name, m.Data)
			}
			for _, dp := range sum.DataPoints {
				name, _ := dp.Attributes.Value(attribute.Key("client"))
				outcome, _ := dp.Attributes.Value(attribute.Key("outcome"))
				if name.AsString() == client {
					out[outcome.AsString()] += dp.Value
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("метрика %s не найдена", httpx.ClientCallsMetric)
	}
	return out
}
