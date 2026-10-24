//go:build integration

package api_test

import (
	"bytes"
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

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth/authtest"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testinfra"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/migrations"
)

// Сквозь HTTP на настоящей базе: проверка клиники, исходы подтверждения, отсутствие фенотипа в ответах
// и соответствие каждого ответа схеме openapi.yaml (kin-openapi).

type checker struct {
	mu       sync.Mutex
	statuses map[uuid.UUID]domain.UnitStatus
	down     bool
}

func (c *checker) CheckUnits(_ context.Context, ids []uuid.UUID) (app.UnitStatusReport, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.down {
		return app.UnitStatusReport{}, app.ErrSourceSystemUnavailable
	}
	out := app.UnitStatusReport{Statuses: map[uuid.UUID]domain.UnitStatus{}}
	for _, id := range ids {
		if s, ok := c.statuses[id]; ok {
			out.Statuses[id] = s
		}
	}
	return out, nil
}

type server struct {
	t       *testing.T
	ctx     context.Context
	svc     *app.Service
	aist    *checker
	idp     *authtest.IdP
	handler http.Handler
	router  routers.Router
	denied  *deniedLog
	version int64
}

func setupServer(t *testing.T) *server {
	t.Helper()
	ctx := context.Background()
	pool, err := bspgx.Connect(ctx, testinfra.Postgres(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := bspgx.Migrate(ctx, pool, messaging.Migrations, refdata.Migrations, migrations.FS); err != nil {
		t.Fatal(err)
	}
	ref := refdata.MustLoad()
	if err := bspgx.WithTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error { return ref.Seed(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewMutable(time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC))
	aist := &checker{statuses: map[uuid.UUID]domain.UnitStatus{}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := app.New(pool, postgres.New(), aist, messaging.NewJournal("transfer", auditc.ModuleTransfer, clk), clk, log,
		app.Config{ExpiryInterval: 5 * time.Second, ExpiryBatch: 100})
	idp := authtest.New(t)
	denied := &deniedLog{}
	mux := http.NewServeMux()
	api.Mount(mux, svc, ref, log, denied.deny)

	doc := loadSpec(t)
	doc.Servers = openapi3.Servers{} // маршрут ищется только по пути
	router, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	return &server{t: t, ctx: ctx, svc: svc, aist: aist, idp: idp, handler: auth.Authenticate(idp.Verifier(t, ""))(mux),
		router: router, denied: denied}
}

func (s *server) token(role auth.Role, clinic *int64) string {
	return s.idp.Token(s.t, authtest.TokenOptions{Username: "u-" + strings.ToLower(string(role)), Roles: []auth.Role{role}, ClinicID: clinic})
}

// call выполняет запрос и проверяет ответ по схеме контракта.
func (s *server) call(method, path, token string, body any) *httptest.ResponseRecorder {
	s.t.Helper()
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			s.t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, r)

	route, params, err := s.router.FindRoute(httptest.NewRequest(method, path, nil))
	if err != nil {
		s.t.Fatalf("%s %s: маршрута нет в контракте: %v", method, path, err)
	}
	in := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: r, PathParams: params, Route: route},
		Status:                 w.Code, Header: w.Header(), Body: io.NopCloser(bytes.NewReader(w.Body.Bytes())),
		Options: &openapi3filter.Options{IncludeResponseStatus: true},
	}
	if err := openapi3filter.ValidateResponse(s.ctx, in); err != nil {
		s.t.Errorf("%s %s → %d: ответ не соответствует openapi.yaml: %v\n%s", method, path, w.Code, err, w.Body.String())
	}
	return w
}

func (s *server) registerUnit(clinic int64) uuid.UUID {
	s.t.Helper()
	id := uuid.New()
	s.aist.mu.Lock()
	s.aist.statuses[id] = domain.UnitInStock
	s.aist.mu.Unlock()
	s.version++
	if err := s.svc.OnUnitRegistered(s.ctx, nil, app.UnitRegistered{UnitID: id, ClinicID: clinic, ComponentKind: "RBC",
		BloodGroup: "A", RhPositive: true, VolumeML: 300, ExpiresAt: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC), Version: s.version}); err != nil {
		s.t.Fatal(err)
	}
	return id
}

func openBody(source int64) map[string]any {
	return map[string]any{
		"choice_id": uuid.NewString(), "request_id": uuid.NewString(), "origin": "DOCTOR_CHOICE",
		"source_clinic_id": source, "target_clinic_id": 3, "component_kind": "RBC", "requested_qty": 2,
		"urgency": "URGENT", "confirm_window_seconds": 900, "fallback_sources": []int64{4},
		"spec": map[string]any{"blood_group": "A", "rh_positive": true, "phenotype": "CcDee", "rule_version": "O15-v3"},
	}
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("разбор ответа: %v\n%s", err, w.Body.String())
	}
	return v
}

type obligationJSON struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	Status       string    `json:"status"`
	Spec         struct {
		PhenotypeRequired bool `json:"phenotype_required"`
	} `json:"spec"`
}

type outcomeJSON struct {
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Units   []uuid.UUID `json:"units"`
}

func TestOpenIsIdempotentAndHidesPhenotype(t *testing.T) {
	s := setupServer(t)
	svcToken := s.token(auth.RoleService, nil)
	body := openBody(1)

	first := s.call(http.MethodPost, "/obligations", svcToken, body)
	if first.Code != http.StatusOK {
		t.Fatalf("открытие: %d %s", first.Code, first.Body.String())
	}
	second := s.call(http.MethodPost, "/obligations", svcToken, body)
	a, b := decode[obligationJSON](t, first), decode[obligationJSON](t, second)
	if a.ObligationID != b.ObligationID {
		t.Fatalf("повтор choice_id открыл второе задание: %s и %s", a.ObligationID, b.ObligationID)
	}
	if !a.Spec.PhenotypeRequired {
		t.Error("признак требования к фенотипу потерян")
	}
	if strings.Contains(first.Body.String(), "CcDee") {
		t.Fatal("фенотип попал в ответ")
	}

	bad := openBody(1)
	bad["target_clinic_id"] = 999999
	if w := s.call(http.MethodPost, "/obligations", svcToken, bad); w.Code != http.StatusBadRequest {
		t.Fatalf("неизвестная клиника: %d", w.Code)
	}
}

func TestOperatorConfirmsOnlyOwnSourceClinic(t *testing.T) {
	s := setupServer(t)
	units := []uuid.UUID{s.registerUnit(1), s.registerUnit(1)}
	opened := decode[obligationJSON](t, s.call(http.MethodPost, "/obligations", s.token(auth.RoleService, nil), openBody(1)))
	path := "/obligations/" + opened.ObligationID.String() + "/confirm"
	body := map[string]any{"unit_ids": units, "idempotency_key": "k-1"}

	stranger := s.call(http.MethodPost, path, s.token(auth.RoleOperator, authtest.Clinic(2)), body)
	if stranger.Code != http.StatusForbidden {
		t.Fatalf("оператор чужой клиники: %d", stranger.Code)
	}
	if len(s.denied.ops) != 1 || s.denied.ops[0] != auth.OpConfirmTransfer {
		t.Fatalf("отказ не записан в аудит: %v", s.denied.ops)
	}
	if w := s.call(http.MethodPost, "/obligations/"+opened.ObligationID.String()+"/replace", s.token(auth.RoleOperator, authtest.Clinic(2)),
		map[string]any{"old_unit_id": units[0], "new_unit_id": units[1]}); w.Code != http.StatusForbidden {
		t.Fatalf("замена оператором чужой клиники: %d", w.Code)
	}

	own := s.call(http.MethodPost, path, s.token(auth.RoleOperator, authtest.Clinic(1)), body)
	if own.Code != http.StatusOK || decode[outcomeJSON](t, own).Code != "CONFIRMED" {
		t.Fatalf("оператор своей клиники: %d %s", own.Code, own.Body.String())
	}
	// Повтор с тем же ключом — тот же исход.
	again := s.call(http.MethodPost, path, s.token(auth.RoleOperator, authtest.Clinic(1)), body)
	if again.Code != http.StatusOK || decode[outcomeJSON](t, again).Code != "CONFIRMED" {
		t.Fatalf("повтор подтверждения: %d %s", again.Code, again.Body.String())
	}
}

func TestConfirmationOutcomesMapToStatusCodes(t *testing.T) {
	s := setupServer(t)
	unit := s.registerUnit(1)
	opened := decode[obligationJSON](t, s.call(http.MethodPost, "/obligations", s.token(auth.RoleService, nil), openBody(1)))
	path := "/obligations/" + opened.ObligationID.String() + "/confirm"
	op := s.token(auth.RoleOperator, authtest.Clinic(1))

	s.aist.mu.Lock()
	s.aist.down = true
	s.aist.mu.Unlock()
	w := s.call(http.MethodPost, path, op, map[string]any{"unit_ids": []uuid.UUID{unit}, "idempotency_key": "k-1"})
	if w.Code != http.StatusServiceUnavailable || decode[outcomeJSON](t, w).Code != "SOURCE_SYSTEM_UNAVAILABLE" {
		t.Fatalf("АИСТ-М не отвечает: %d %s", w.Code, w.Body.String())
	}
	s.aist.mu.Lock()
	s.aist.down = false
	s.aist.mu.Unlock()

	w = s.call(http.MethodPost, path, op, map[string]any{"unit_ids": []uuid.UUID{uuid.New()}, "idempotency_key": "k-2"})
	if w.Code != http.StatusUnprocessableEntity || decode[outcomeJSON](t, w).Code != "INVALID_REQUEST" {
		t.Fatalf("чужая единица: %d %s", w.Code, w.Body.String())
	}

	w = s.call(http.MethodPost, "/obligations/"+uuid.NewString()+"/confirm", op, map[string]any{"unit_ids": []uuid.UUID{unit}, "idempotency_key": "k-3"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("неизвестное задание: %d", w.Code)
	}

	cancel := s.call(http.MethodPost, "/obligations/"+opened.ObligationID.String()+"/cancel", s.token(auth.RoleDuty, nil),
		map[string]any{"reason": "DUTY_OFFICER_DECISION"})
	if cancel.Code != http.StatusOK {
		t.Fatalf("отмена дежурным: %d %s", cancel.Code, cancel.Body.String())
	}
	w = s.call(http.MethodPost, path, op, map[string]any{"unit_ids": []uuid.UUID{unit}, "idempotency_key": "k-4"})
	if w.Code != http.StatusConflict || decode[outcomeJSON](t, w).Code != "TASK_WITHDRAWN" {
		t.Fatalf("отменённое задание: %d %s", w.Code, w.Body.String())
	}
	if w := s.call(http.MethodPost, "/obligations/"+uuid.NewString()+"/cancel", s.token(auth.RoleDuty, nil),
		map[string]any{"reason": "DUTY_OFFICER_DECISION"}); w.Code != http.StatusNotFound {
		t.Fatalf("отмена неизвестного задания: %d", w.Code)
	}
}

func TestViewIsLimitedToParticipatingClinics(t *testing.T) {
	s := setupServer(t)
	opened := decode[obligationJSON](t, s.call(http.MethodPost, "/obligations", s.token(auth.RoleService, nil), openBody(1)))
	path := "/obligations/" + opened.ObligationID.String()

	if w := s.call(http.MethodGet, path, s.token(auth.RolePhysician, authtest.Clinic(3)), nil); w.Code != http.StatusOK {
		t.Fatalf("врач клиники-получателя: %d", w.Code)
	}
	if w := s.call(http.MethodGet, path, s.token(auth.RoleOperator, authtest.Clinic(7)), nil); w.Code != http.StatusForbidden {
		t.Fatalf("оператор посторонней клиники: %d", w.Code)
	}
	if w := s.call(http.MethodGet, "/obligations?sourceClinicId=1", s.token(auth.RoleOperator, authtest.Clinic(7)), nil); w.Code != http.StatusForbidden {
		t.Fatalf("список чужого источника: %d", w.Code)
	}
	list := s.call(http.MethodGet, "/obligations?sourceClinicId=1&status=PENDING", s.token(auth.RoleOperator, authtest.Clinic(1)), nil)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), opened.ObligationID.String()) {
		t.Fatalf("список своего источника: %d %s", list.Code, list.Body.String())
	}
	if w := s.call(http.MethodGet, "/obligations", s.token(auth.RoleDuty, nil), nil); w.Code != http.StatusBadRequest {
		t.Fatalf("список без фильтра: %d", w.Code)
	}
}

func TestRefusalAndHistoryRespectClinicAndContract(t *testing.T) {
	s := setupServer(t)
	opened := decode[obligationJSON](t, s.call(http.MethodPost, "/obligations", s.token(auth.RoleService, nil), openBody(1)))
	base := "/obligations/" + opened.ObligationID.String()
	stranger := s.token(auth.RoleOperator, authtest.Clinic(7))
	if w := s.call(http.MethodGet, base+"/history", stranger, nil); w.Code != 403 {
		t.Fatalf("история чужой клиники: %d", w.Code)
	}
	if w := s.call(http.MethodPost, base+"/reject", stranger, map[string]string{"reason": "SOURCE_REFUSED"}); w.Code != 403 {
		t.Fatalf("отказ чужой клиники: %d", w.Code)
	}
	operator := s.token(auth.RoleOperator, authtest.Clinic(1))
	for range 2 {
		if w := s.call(http.MethodPost, base+"/reject", operator, map[string]string{"reason": "SOURCE_REFUSED"}); w.Code != 200 {
			t.Fatalf("отказ: %d %s", w.Code, w.Body.String())
		}
	}
	w := s.call(http.MethodGet, base+"/history?limit=1&offset=0", operator, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "REJECTED") || strings.Contains(w.Body.String(), "CcDee") {
		t.Fatalf("история отказа: %d %s", w.Code, w.Body.String())
	}
	if w := s.call(http.MethodGet, base+"/history?limit=101", operator, nil); w.Code != 400 {
		t.Fatalf("неограниченная история: %d", w.Code)
	}
}
