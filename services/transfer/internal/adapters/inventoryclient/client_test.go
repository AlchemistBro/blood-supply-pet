package inventoryclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/adapters/inventoryclient"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

type unitJSON struct {
	UnitID        uuid.UUID `json:"unit_id"`
	ClinicID      int64     `json:"clinic_id"`
	ComponentKind string    `json:"component_kind"`
	Status        string    `json:"status"`
	Available     bool      `json:"available"`
	Version       int64     `json:"version"`
	ChangedAt     time.Time `json:"changed_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

type reportJSON struct {
	Source    string      `json:"source"`
	CheckedAt time.Time   `json:"checked_at"`
	Watermark *time.Time  `json:"watermark"`
	Units     []unitJSON  `json:"units"`
	NotFound  []uuid.UUID `json:"not_found"`
}

// staticToken — служебный токен для тестов (в сервисе — auth.ServiceToken).
type staticToken string

func (s staticToken) Authorize(r *http.Request) error {
	r.Header.Set("Authorization", "Bearer "+string(s))
	return nil
}

func unit(id uuid.UUID, status string, available bool) unitJSON {
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	return unitJSON{UnitID: id, ClinicID: 1, ComponentKind: "RBC", Status: status, Available: available, Version: 1,
		ChangedAt: now, ExpiresAt: now.Add(240 * time.Hour)}
}

func newClient(t *testing.T, url string, timeout time.Duration) *inventoryclient.Client {
	t.Helper()
	c, err := inventoryclient.New(inventoryclient.Config{BaseURL: url, Timeout: timeout, BreakerFailures: 3, BreakerOpen: time.Minute},
		staticToken("svc-token"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestStatusesFromAIST(t *testing.T) {
	inStock, quarantined, odd, missing := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	var gotAuth, gotIDs string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/units/status" {
			http.NotFound(w, r)
			return
		}
		gotAuth, gotIDs = r.Header.Get("Authorization"), r.URL.Query().Get("ids")
		_ = json.NewEncoder(w).Encode(reportJSON{
			Source: "aist", CheckedAt: time.Now().UTC(),
			// IN_STOCK, но available=false: доверяем справочнику, единица недоступна.
			Units:    []unitJSON{unit(inStock, "IN_STOCK", true), unit(quarantined, "QUARANTINE", false), unit(odd, "IN_STOCK", false)},
			NotFound: []uuid.UUID{missing},
		})
	}))
	defer srv.Close()

	rep, err := newClient(t, srv.URL, time.Second).CheckUnits(context.Background(), []uuid.UUID{inStock, quarantined, odd, missing})
	if err != nil {
		t.Fatal(err)
	}
	if rep.FromCopy {
		t.Error("source=aist не должен давать FromCopy")
	}
	if rep.Statuses[inStock] != domain.UnitInStock || !rep.Statuses[inStock].Allocatable() {
		t.Errorf("в наличии: %q", rep.Statuses[inStock])
	}
	if rep.Statuses[quarantined] != domain.UnitQuarantine {
		t.Errorf("карантин: %q", rep.Statuses[quarantined])
	}
	if s, ok := rep.Statuses[odd]; ok && s.Allocatable() {
		t.Errorf("available=false не должно давать доступную единицу: %q", s)
	}
	if _, ok := rep.Statuses[missing]; ok {
		t.Error("не найденная единица не должна попадать в ответ")
	}
	if gotAuth != "Bearer svc-token" {
		t.Errorf("служебный токен: %q", gotAuth)
	}
	if strings.Count(gotIDs, ",") != 3 {
		t.Errorf("ids через запятую: %q", gotIDs)
	}
}

func TestCopySourceMarksFromCopy(t *testing.T) {
	id := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(reportJSON{Source: "copy", CheckedAt: time.Now().UTC(), Units: []unitJSON{unit(id, "IN_STOCK", true)}, NotFound: []uuid.UUID{}})
	}))
	defer srv.Close()
	rep, err := newClient(t, srv.URL, time.Second).CheckUnits(context.Background(), []uuid.UUID{id})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.FromCopy {
		t.Fatal("source=copy должен давать FromCopy: вслепую не закрепляем (ADR-0004)")
	}
}

func TestTimeoutIsSourceSystemUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	start := time.Now()
	_, err := newClient(t, srv.URL, 100*time.Millisecond).CheckUnits(context.Background(), []uuid.UUID{uuid.New()})
	if !errors.Is(err, app.ErrSourceSystemUnavailable) {
		t.Fatalf("ожидали ErrSourceSystemUnavailable, получили %v", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("таймаут не сработал: %s", time.Since(start))
	}
}

// После нескольких отказов подряд предохранитель размыкается: inventory больше не вызывается.
func TestCircuitBreakerStopsCalls(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := newClient(t, srv.URL, time.Second)
	for i := 0; i < 6; i++ {
		if _, err := c.CheckUnits(context.Background(), []uuid.UUID{uuid.New()}); !errors.Is(err, app.ErrSourceSystemUnavailable) {
			t.Fatalf("вызов %d: ожидали ErrSourceSystemUnavailable, получили %v", i, err)
		}
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("inventory вызван %d раз, ожидали 3 (дальше предохранитель разомкнут)", n)
	}
}

// Отказ в доступе — тоже «статус не получен»: подтверждать вслепую нельзя.
func TestForbiddenIsSourceSystemUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := newClient(t, srv.URL, time.Second).CheckUnits(context.Background(), []uuid.UUID{uuid.New()})
	if !errors.Is(err, app.ErrSourceSystemUnavailable) {
		t.Fatalf("ожидали ErrSourceSystemUnavailable, получили %v", err)
	}
}

// Больше 1000 единиц — несколько запросов по 1000 (ограничение контракта inventory).
func TestLargeRequestIsSplitIntoBatches(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		ids := strings.Split(r.URL.Query().Get("ids"), ",")
		if len(ids) > 1000 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		rep := reportJSON{Source: "aist", CheckedAt: time.Now().UTC(), Units: []unitJSON{}, NotFound: []uuid.UUID{}}
		for _, s := range ids {
			rep.Units = append(rep.Units, unit(uuid.MustParse(s), "IN_STOCK", true))
		}
		_ = json.NewEncoder(w).Encode(rep)
	}))
	defer srv.Close()
	ids := make([]uuid.UUID, 2500)
	for i := range ids {
		ids[i] = uuid.New()
	}
	rep, err := newClient(t, srv.URL, time.Second).CheckUnits(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || len(rep.Statuses) != 2500 {
		t.Fatalf("запросов %d, статусов %d", calls.Load(), len(rep.Statuses))
	}
}
