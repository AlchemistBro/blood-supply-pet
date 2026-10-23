package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-emergency/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-emergency/internal/sender"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-emergency/internal/wire"
)

var now = time.Date(2026, 10, 23, 9, 17, 0, 0, time.UTC)

type staticToken string

func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

// planningStub — приёмник ИНТ-02 вместо planning: запоминает запросы, отвечает заданным кодом.
type planningStub struct {
	mu     sync.Mutex
	status int
	auth   []string
	bodies [][]byte
	paths  []string
}

func (p *planningStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	p.auth = append(p.auth, r.Header.Get("Authorization"))
	p.bodies = append(p.bodies, b)
	p.paths = append(p.paths, r.Method+" "+r.URL.Path)
	status := p.status
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"receipt":"демо"}`)
}

func (p *planningStub) calls() ([]string, [][]byte, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.auth...), append([][]byte(nil), p.bodies...), append([]string(nil), p.paths...)
}

type fixture struct {
	sim      *httptest.Server
	planning *planningStub
}

func newFixture(t *testing.T, tokens sender.TokenSource, status int) *fixture {
	t.Helper()
	stub := &planningStub{status: status}
	planning := httptest.NewServer(stub)
	t.Cleanup(planning.Close)
	h := api.NewHandler(api.Deps{
		Ref:         refdata.MustLoad(),
		Clock:       clock.NewMutable(now),
		Sender:      sender.New(planning.URL, tokens, 2*time.Second),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		DefaultSeed: 7,
	})
	sim := httptest.NewServer(h)
	t.Cleanup(sim.Close)
	return &fixture{sim: sim, planning: stub}
}

func (f *fixture) post(t *testing.T, query string) (int, api.Record) {
	t.Helper()
	resp, err := http.Post(f.sim.URL+"/dev/forecast?"+query, "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var rec api.Record
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
			t.Fatalf("ответ имитатора: %v", err)
		}
	}
	return resp.StatusCode, rec
}

func hasAnyKey(t *testing.T, body []byte, names []string) []string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("тело прогноза не JSON: %v", err)
	}
	var found []string
	for _, n := range names {
		if _, ok := m[n]; ok {
			found = append(found, n)
		}
	}
	return found
}

func TestForecastIsPostedToPlanningWithServiceToken(t *testing.T) {
	f := newFixture(t, staticToken("svc-token"), http.StatusAccepted)
	status, rec := f.post(t, "scenario=FLOOD&seed=5&scale=2")
	if status != http.StatusOK {
		t.Fatalf("POST /dev/forecast: %d", status)
	}
	auth, bodies, paths := f.planning.calls()
	if len(bodies) != 1 || paths[0] != "POST "+sender.ForecastPath || auth[0] != "Bearer svc-token" {
		t.Fatalf("planning получил %v, Authorization %v", paths, auth)
	}
	if found := hasAnyKey(t, bodies[0], wire.PIIFields); len(found) != 0 {
		t.Fatalf("без inject в прогнозе поля ПДн %v (NFR010)", found)
	}
	var p wire.Payload
	if err := json.Unmarshal(bodies[0], &p); err != nil {
		t.Fatal(err)
	}
	if p.ScenarioVersion == "" || p.ScenarioVersion != rec.ScenarioVersion || p.ScenarioKind != "FLOOD" || !p.Demo || len(p.Lines) == 0 {
		t.Fatalf("прогноз %+v, запись %+v", p, rec)
	}
	if rec.PlanningStatus != http.StatusAccepted || rec.LinesCount != len(p.Lines) || rec.Inject != "" || !rec.SentAt.Equal(now) {
		t.Fatalf("запись об отправке %+v", rec)
	}
}

func TestInjectPIIAddsForbiddenFields(t *testing.T) {
	f := newFixture(t, staticToken("svc-token"), http.StatusAccepted)
	if status, rec := f.post(t, "inject=pii"); status != http.StatusOK || rec.Inject != "pii" {
		t.Fatalf("inject=pii: %d %+v", status, rec)
	}
	_, bodies, _ := f.planning.calls()
	found := hasAnyKey(t, bodies[0], wire.PIIFields)
	if len(found) != len(wire.PIIFields) {
		t.Fatalf("inject=pii добавил только %v", found)
	}
}

func TestSameSeedGivesSameForecast(t *testing.T) {
	f := newFixture(t, staticToken("svc-token"), http.StatusAccepted)
	f.post(t, "scenario=INDUSTRIAL_EXPLOSION&seed=11&receivers=false")
	f.post(t, "scenario=INDUSTRIAL_EXPLOSION&seed=11&receivers=false")
	f.post(t, "scenario=INDUSTRIAL_EXPLOSION&seed=12&receivers=false")
	_, bodies, _ := f.planning.calls()
	if len(bodies) != 3 {
		t.Fatalf("отправок %d", len(bodies))
	}
	if string(bodies[0]) != string(bodies[1]) {
		t.Fatal("один seed дал разные прогнозы")
	}
	if string(bodies[0]) == string(bodies[2]) {
		t.Fatal("разные seed дали одинаковые прогнозы")
	}
	if strings.Contains(string(bodies[0]), "clinic_code") {
		t.Fatal("receivers=false, а в строках есть принимающие клиники")
	}
}

func TestWithoutTokenNothingIsSent(t *testing.T) {
	f := newFixture(t, sender.Unconfigured{}, http.StatusAccepted)
	if status, _ := f.post(t, ""); status != http.StatusServiceUnavailable {
		t.Fatalf("без токена ожидали 503, получили %d", status)
	}
	if _, bodies, _ := f.planning.calls(); len(bodies) != 0 {
		t.Fatal("без токена прогноз ушёл в planning")
	}
}

func TestNoAuthSendsWithoutHeader(t *testing.T) {
	f := newFixture(t, sender.NoAuth{}, http.StatusAccepted)
	if status, _ := f.post(t, ""); status != http.StatusOK {
		t.Fatalf("без токена (локально): %d", status)
	}
	if auth, _, _ := f.planning.calls(); len(auth) != 1 || auth[0] != "" {
		t.Fatalf("Authorization %v", auth)
	}
}

func TestPlanningRejectionIsRecorded(t *testing.T) {
	f := newFixture(t, staticToken("svc-token"), http.StatusConflict)
	status, rec := f.post(t, "version=dup-1")
	if status != http.StatusOK || rec.PlanningStatus != http.StatusConflict || rec.ScenarioVersion != "dup-1" {
		t.Fatalf("отказ planning: %d %+v", status, rec)
	}
}

func TestBadParametersAreRejected(t *testing.T) {
	f := newFixture(t, staticToken("svc-token"), http.StatusAccepted)
	for _, q := range []string{"inject=sql", "scale=0", "scale=abc", "seed=x", "scenario=METEOR", "receivers=maybe"} {
		if status, _ := f.post(t, q); status != http.StatusBadRequest {
			t.Fatalf("%s: ожидали 400, получили %d", q, status)
		}
	}
	if _, bodies, _ := f.planning.calls(); len(bodies) != 0 {
		t.Fatal("некорректный запрос ушёл в planning")
	}
}

func TestForecastHistory(t *testing.T) {
	f := newFixture(t, staticToken("svc-token"), http.StatusAccepted)
	f.post(t, "seed=1")
	f.post(t, "seed=2&inject=pii")
	resp, err := http.Get(f.sim.URL + "/dev/forecasts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list api.RecordList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Forecasts) != 2 || list.Forecasts[0].Seed != 2 || list.Forecasts[1].Seed != 1 || !list.Demo {
		t.Fatalf("история %+v", list)
	}
}

func TestHealthAndScenarios(t *testing.T) {
	f := newFixture(t, sender.NoAuth{}, http.StatusAccepted)
	for _, path := range []string{"/healthz", "/dev/scenarios"} {
		resp, err := http.Get(f.sim.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
	}
}
