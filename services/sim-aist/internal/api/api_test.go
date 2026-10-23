package api_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-aist/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-aist/internal/state"
)

var t0 = time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)

type env struct {
	t      *testing.T
	h      http.Handler
	clk    *clock.Mutable
	ref    *refdata.Set
	logBuf *syncBuffer
	slept  []time.Duration
	mu     sync.Mutex
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, clk: clock.NewMutable(t0), ref: refdata.MustLoad(), logBuf: &syncBuffer{}}
	log := slog.New(slog.NewJSONHandler(e.logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	store := state.New(e.clk, e.ref)
	e.h = api.New(store, e.ref, e.clk, log, api.Options{
		Seed: api.SeedDefaults{Seed: 42, Units: 30, WithdrawnShare: 0.1},
		Sleep: func(_ context.Context, d time.Duration) error {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.slept = append(e.slept, d)
			return nil
		},
	}).Routes()
	return e
}

func (e *env) do(method, path string, body any, headers ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd *bytes.Reader
	switch b := body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case string:
		rd = bytes.NewReader([]byte(b))
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("тело ответа %d не JSON: %v: %s", rec.Code, err, rec.Body.String())
	}
	return v
}

func expect(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("ожидали %d, получили %d: %s", code, rec.Code, rec.Body.String())
	}
}

func (e *env) createUnit(code string) state.Unit {
	e.t.Helper()
	rec := e.do(http.MethodPost, "/dev/units", map[string]any{
		"aist_unit_code": code, "clinic_code": e.ref.Clinics[0].ClinicCode,
		"component_kind": "RBC", "blood_group": "A+",
	})
	expect(e.t, rec, http.StatusCreated)
	return decode[state.Unit](e.t, rec)
}

type statusResponse struct {
	Units []struct {
		AistUnitCode     string    `json:"aist_unit_code"`
		Status           string    `json:"status"`
		WithdrawalReason string    `json:"withdrawal_reason"`
		Version          int64     `json:"version"`
		ChangedAt        time.Time `json:"changed_at"`
	} `json:"units"`
	NotFound  []string  `json:"not_found"`
	CheckedAt time.Time `json:"checked_at"`
}

type changesResponse struct {
	Units          []state.Unit `json:"units"`
	LastVersion    int64        `json:"last_version"`
	CurrentVersion int64        `json:"current_version"`
	HasMore        bool         `json:"has_more"`
}

func TestWithdrawChangesStatusAndVersion(t *testing.T) {
	e := newEnv(t)
	created := e.createUnit("U-1")
	if created.Status != state.StatusInStock {
		t.Fatalf("новая единица: %s", created.Status)
	}
	rec := e.do(http.MethodPost, "/dev/units/U-1/withdraw", map[string]string{"reason": "ISSUED"})
	expect(t, rec, http.StatusOK)

	rec = e.do(http.MethodGet, "/units/status?ids=U-1", nil)
	expect(t, rec, http.StatusOK)
	st := decode[statusResponse](t, rec)
	if len(st.Units) != 1 {
		t.Fatalf("ожидали одну единицу: %+v", st)
	}
	got := st.Units[0]
	if got.Status != state.StatusIssued || got.WithdrawalReason != state.ReasonIssued {
		t.Fatalf("после выдачи статус %s, причина %s", got.Status, got.WithdrawalReason)
	}
	if got.Version <= created.Version {
		t.Fatalf("версия не выросла: %d → %d", created.Version, got.Version)
	}
	// Выбывшая единица остаётся в состоянии, повторное выбытие — конфликт.
	expect(t, e.do(http.MethodPost, "/dev/units/U-1/withdraw", map[string]string{"reason": "DISCARDED"}), http.StatusConflict)
}

func TestWithdrawDefaultsToIssuedAndValidatesReason(t *testing.T) {
	e := newEnv(t)
	e.createUnit("U-1")
	e.createUnit("U-2")
	rec := e.do(http.MethodPost, "/dev/units/U-1/withdraw", nil)
	expect(t, rec, http.StatusOK)
	if u := decode[state.Unit](t, rec); u.Status != state.StatusIssued {
		t.Fatalf("причина по умолчанию: %s", u.Status)
	}
	rec = e.do(http.MethodPost, "/dev/units/U-2/withdraw", map[string]string{"reason": "LOST"})
	expect(t, rec, http.StatusBadRequest)
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("ошибка не в problem+json: %s", ct)
	}
	expect(t, e.do(http.MethodPost, "/dev/units/U-404/withdraw", nil), http.StatusNotFound)
}

func TestUnavailableForGives503ForGivenTime(t *testing.T) {
	e := newEnv(t)
	e.createUnit("U-1")
	expect(t, e.do(http.MethodPost, "/dev/faults", map[string]any{"unavailableFor": "30s"}), http.StatusOK)

	for _, path := range []string{"/units/status?ids=U-1", "/units/changes?since=0", "/snapshot.csv"} {
		rec := e.do(http.MethodGet, path, nil)
		expect(t, rec, http.StatusServiceUnavailable)
		if ra := rec.Header().Get("Retry-After"); ra != "30" {
			t.Fatalf("%s: Retry-After %q, ожидали 30", path, ra)
		}
	}
	expect(t, e.do(http.MethodPost, "/allocations", map[string]any{}), http.StatusServiceUnavailable)
	// Служебные и dev-маршруты сбой не затрагивает: иначе сбой нельзя снять.
	expect(t, e.do(http.MethodGet, "/healthz", nil), http.StatusOK)

	e.clk.Advance(29 * time.Second)
	expect(t, e.do(http.MethodGet, "/units/status?ids=U-1", nil), http.StatusServiceUnavailable)
	e.clk.Advance(time.Second)
	expect(t, e.do(http.MethodGet, "/units/status?ids=U-1", nil), http.StatusOK)
}

func TestFaultsCanBeCleared(t *testing.T) {
	e := newEnv(t)
	expect(t, e.do(http.MethodPost, "/dev/faults", map[string]any{"unavailableFor": "1h"}), http.StatusOK)
	expect(t, e.do(http.MethodGet, "/units/changes", nil), http.StatusServiceUnavailable)
	expect(t, e.do(http.MethodPost, "/dev/faults", map[string]any{}), http.StatusOK)
	expect(t, e.do(http.MethodGet, "/units/changes", nil), http.StatusOK)
	expect(t, e.do(http.MethodPost, "/dev/faults", map[string]any{"unavailableFor": "скоро"}), http.StatusBadRequest)
}

func TestSlowMsDelaysResponse(t *testing.T) {
	e := newEnv(t)
	expect(t, e.do(http.MethodPost, "/dev/faults", map[string]any{"slowMs": 250}), http.StatusOK)
	expect(t, e.do(http.MethodGet, "/units/changes", nil), http.StatusOK)
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.slept) != 1 || e.slept[0] != 250*time.Millisecond {
		t.Fatalf("ожидали одну задержку 250 мс, получили %v", e.slept)
	}
}

func TestChangesSinceReturnsOnlyNewVersions(t *testing.T) {
	e := newEnv(t)
	e.createUnit("U-1")
	u2 := e.createUnit("U-2")
	e.createUnit("U-3")
	expect(t, e.do(http.MethodPost, "/dev/units/U-1/withdraw", nil), http.StatusOK)

	rec := e.do(http.MethodGet, "/units/changes?since="+itoa(u2.Version), nil)
	expect(t, rec, http.StatusOK)
	ch := decode[changesResponse](t, rec)
	if len(ch.Units) != 2 || ch.Units[0].AistUnitCode != "U-3" || ch.Units[1].AistUnitCode != "U-1" {
		t.Fatalf("ожидали U-3 и U-1 по возрастанию версии: %+v", ch.Units)
	}
	for _, u := range ch.Units {
		if u.Version <= u2.Version {
			t.Fatalf("в выдаче версия не новее since: %d", u.Version)
		}
	}
	if ch.LastVersion != ch.Units[1].Version || ch.CurrentVersion != ch.LastVersion || ch.HasMore {
		t.Fatalf("курсор: last=%d current=%d more=%v", ch.LastVersion, ch.CurrentVersion, ch.HasMore)
	}

	rec = e.do(http.MethodGet, "/units/changes?since=0&limit=2", nil)
	expect(t, rec, http.StatusOK)
	page := decode[changesResponse](t, rec)
	if len(page.Units) != 2 || !page.HasMore {
		t.Fatalf("лимит 2: %d единиц, продолжение %v", len(page.Units), page.HasMore)
	}

	rec = e.do(http.MethodGet, "/units/changes?since="+itoa(ch.LastVersion), nil)
	expect(t, rec, http.StatusOK)
	if empty := decode[changesResponse](t, rec); len(empty.Units) != 0 || empty.LastVersion != ch.LastVersion {
		t.Fatalf("после последней версии: %+v", empty)
	}

	expect(t, e.do(http.MethodGet, "/units/changes?since=abc", nil), http.StatusBadRequest)
	expect(t, e.do(http.MethodGet, "/units/changes?limit=0", nil), http.StatusBadRequest)
}

func TestQuarantineIsVisibleInStatus(t *testing.T) {
	e := newEnv(t)
	e.createUnit("U-1")
	rec := e.do(http.MethodPost, "/dev/units/U-1/status", map[string]string{"status": "QUARANTINE"})
	expect(t, rec, http.StatusOK)

	rec = e.do(http.MethodGet, "/units/status?ids=U-1,U-404", nil)
	expect(t, rec, http.StatusOK)
	st := decode[statusResponse](t, rec)
	if len(st.Units) != 1 || st.Units[0].Status != state.StatusQuarantine {
		t.Fatalf("ожидали QUARANTINE: %+v", st.Units)
	}
	if len(st.NotFound) != 1 || st.NotFound[0] != "U-404" {
		t.Fatalf("неизвестный код: %v", st.NotFound)
	}
	if !st.CheckedAt.Equal(t0) {
		t.Fatalf("время проверки не из Clock: %s", st.CheckedAt)
	}
	// Из карантина единицу нельзя выдать (ПП РФ № 641, п. 62), но можно вернуть в запас.
	expect(t, e.do(http.MethodPost, "/dev/units/U-1/withdraw", map[string]string{"reason": "ISSUED"}), http.StatusConflict)
	expect(t, e.do(http.MethodPost, "/dev/units/U-1/status", map[string]string{"status": "IN_STOCK"}), http.StatusOK)
	expect(t, e.do(http.MethodPost, "/dev/units/U-1/status", map[string]string{"status": "BROKEN"}), http.StatusBadRequest)
}

func TestStatusRequiresIDs(t *testing.T) {
	e := newEnv(t)
	expect(t, e.do(http.MethodGet, "/units/status", nil), http.StatusBadRequest)
	expect(t, e.do(http.MethodGet, "/units/status?ids=", nil), http.StatusBadRequest)
}

func TestAllocationRepeatWithSameKeyDoesNotCreateSecond(t *testing.T) {
	e := newEnv(t)
	e.createUnit("U-1")
	e.createUnit("U-2")
	const caseNo = "CASE-SECRET-777"
	body := map[string]any{
		"patient_case_no": caseNo, "recipient_clinic_code": e.ref.Clinics[1].ClinicCode,
		"aist_unit_codes": []string{"U-1", "U-2"},
	}
	first := e.do(http.MethodPost, "/allocations", body, "Idempotency-Key", "o07-1:3")
	expect(t, first, http.StatusCreated)
	second := e.do(http.MethodPost, "/allocations", body, "Idempotency-Key", "o07-1:3")
	expect(t, second, http.StatusCreated)
	if first.Body.String() != second.Body.String() {
		t.Fatalf("повтор дал другой ответ:\n%s\n%s", first.Body.String(), second.Body.String())
	}
	if second.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatal("повтор не помечен заголовком Idempotent-Replayed")
	}

	rec := e.do(http.MethodGet, "/dev/allocations", nil)
	expect(t, rec, http.StatusOK)
	list := decode[struct {
		Allocations []map[string]any `json:"allocations"`
	}](t, rec)
	if len(list.Allocations) != 1 {
		t.Fatalf("закреплений %d, ожидали 1", len(list.Allocations))
	}
	// Номер случая — K1: не возвращается наружу и не попадает в логи.
	for _, b := range []string{first.Body.String(), rec.Body.String(), e.logBuf.String()} {
		if strings.Contains(b, caseNo) {
			t.Fatalf("номер случая утёк: %s", b)
		}
	}

	changed := map[string]any{
		"patient_case_no": caseNo, "recipient_clinic_code": e.ref.Clinics[1].ClinicCode,
		"aist_unit_codes": []string{"U-1"},
	}
	expect(t, e.do(http.MethodPost, "/allocations", changed, "Idempotency-Key", "o07-1:3"), http.StatusUnprocessableEntity)
}

func TestAllocationKeyFromBodyAndValidation(t *testing.T) {
	e := newEnv(t)
	e.createUnit("U-1")
	clinic := e.ref.Clinics[0].ClinicCode
	expect(t, e.do(http.MethodPost, "/allocations", map[string]any{
		"patient_case_no": "C-1", "recipient_clinic_code": clinic, "aist_unit_codes": []string{"U-1"},
	}), http.StatusBadRequest) // нет ключа
	expect(t, e.do(http.MethodPost, "/allocations", map[string]any{
		"idempotency_key": "k-1", "patient_case_no": "C-1", "recipient_clinic_code": clinic,
		"aist_unit_codes": []string{"U-404"},
	}), http.StatusUnprocessableEntity) // нет единицы
	expect(t, e.do(http.MethodPost, "/allocations", map[string]any{
		"idempotency_key": "k-2", "patient_case_no": "C-1", "recipient_clinic_code": clinic,
		"aist_unit_codes": []string{"U-1"},
	}, "Idempotency-Key", "k-3"), http.StatusBadRequest) // ключи расходятся
	expect(t, e.do(http.MethodPost, "/allocations", map[string]any{
		"idempotency_key": "k-4", "patient_case_no": "C-1", "recipient_clinic_code": clinic,
		"aist_unit_codes": []string{"U-1"},
	}), http.StatusCreated)
	expect(t, e.do(http.MethodPost, "/allocations", map[string]any{
		"idempotency_key": "k-5", "patient_case_no": "C-2", "recipient_clinic_code": clinic,
		"aist_unit_codes": []string{"U-1"},
	}), http.StatusConflict) // единица уже закреплена
}

func TestCreateUnitValidation(t *testing.T) {
	e := newEnv(t)
	e.createUnit("U-1")
	expect(t, e.do(http.MethodPost, "/dev/units", map[string]any{
		"aist_unit_code": "U-1", "clinic_code": e.ref.Clinics[0].ClinicCode, "component_kind": "RBC", "blood_group": "A+",
	}), http.StatusConflict)
	expect(t, e.do(http.MethodPost, "/dev/units", map[string]any{
		"clinic_code": "NOPE", "component_kind": "RBC", "blood_group": "A+",
	}), http.StatusBadRequest)
	expect(t, e.do(http.MethodPost, "/dev/units", `{"clinic_code":`), http.StatusBadRequest)
	rec := e.do(http.MethodPost, "/dev/units", map[string]any{
		"clinic_code": e.ref.Clinics[0].ClinicCode, "component_kind": "PLT", "blood_group": "O-", "status": "QUARANTINE",
	})
	expect(t, rec, http.StatusCreated)
	u := decode[state.Unit](t, rec)
	if u.AistUnitCode == "" || u.Status != state.StatusQuarantine || u.VolumeML != 200 {
		t.Fatalf("значения по умолчанию: %+v", u)
	}
}

func TestSnapshotCSV(t *testing.T) {
	e := newEnv(t)
	e.createUnit("U-2")
	e.createUnit("U-1")
	expect(t, e.do(http.MethodPost, "/dev/units/U-2/withdraw", map[string]string{"reason": "EXPIRED"}), http.StatusOK)
	rec := e.do(http.MethodGet, "/snapshot.csv", nil)
	expect(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("тип содержимого: %s", ct)
	}
	rows, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0][0] != "aist_unit_code" {
		t.Fatalf("ожидали заголовок и две строки: %v", rows)
	}
	if rows[1][0] != "U-1" || rows[2][0] != "U-2" {
		t.Fatalf("строки не по коду: %v", rows)
	}
	idx := map[string]int{}
	for i, h := range rows[0] {
		idx[h] = i
	}
	if rows[2][idx["status"]] != state.StatusDiscarded || rows[2][idx["withdrawal_reason"]] != state.ReasonExpired {
		t.Fatalf("выбывшая единица в снимке: %v", rows[2])
	}
}

func TestSeedLoadsDatasetOnce(t *testing.T) {
	e := newEnv(t)
	rec := e.do(http.MethodPost, "/dev/seed", nil)
	expect(t, rec, http.StatusOK)
	res := decode[struct {
		Created int   `json:"created"`
		Skipped int   `json:"skipped"`
		Version int64 `json:"version"`
	}](t, rec)
	if res.Created != 30 || res.Skipped != 0 || res.Version != 30 {
		t.Fatalf("сид по умолчанию: %+v", res)
	}
	rec = e.do(http.MethodPost, "/dev/seed", nil)
	expect(t, rec, http.StatusOK)
	again := decode[struct {
		Created int `json:"created"`
		Skipped int `json:"skipped"`
	}](t, rec)
	if again.Created != 0 || again.Skipped != 30 {
		t.Fatalf("повторный сид: %+v", again)
	}
	// Другой сид: коды вида AIST-<клиника>-<номер> могут совпасть с уже загруженными, такие пропускаются.
	rec = e.do(http.MethodPost, "/dev/seed", map[string]any{"seed": 7, "units": 5})
	expect(t, rec, http.StatusOK)
	other := decode[struct {
		Created int `json:"created"`
		Skipped int `json:"skipped"`
	}](t, rec)
	if other.Created+other.Skipped != 5 {
		t.Fatalf("второй набор: %+v", other)
	}
	expect(t, e.do(http.MethodPost, "/dev/seed", map[string]any{"units": -1}), http.StatusBadRequest)

	rec = e.do(http.MethodGet, "/units/changes?since=0&limit=5000", nil)
	expect(t, rec, http.StatusOK)
	if ch := decode[changesResponse](t, rec); len(ch.Units) != 30+other.Created {
		t.Fatalf("после двух сидов единиц %d, ожидали %d", len(ch.Units), 30+other.Created)
	}
}

func TestHealthz(t *testing.T) {
	e := newEnv(t)
	rec := e.do(http.MethodGet, "/healthz", nil)
	expect(t, rec, http.StatusOK)
	if rec.Header().Get(httpx.HeaderCorrelationID) == "" {
		t.Fatal("нет корреляции в ответе")
	}
}

func itoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
