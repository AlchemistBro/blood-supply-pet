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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth/authtest"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testinfra"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/adapters/aistclient"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/adapters/events"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/api/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/fakeaist"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/migrations"
)

var t0 = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)

type denials struct {
	mu  sync.Mutex
	ops []auth.Operation
}

func (d *denials) record(_ *http.Request, _ *auth.Principal, op auth.Operation) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ops = append(d.ops, op)
}

func (d *denials) list() []auth.Operation {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]auth.Operation(nil), d.ops...)
}

type env struct {
	server *httptest.Server
	idp    *authtest.IdP
	aist   *fakeaist.Server
	svc    *app.Service
	pool   *pgxpool.Pool
	clock  *clock.Mutable
	denied *denials
}

func setup(t *testing.T, dev bool) env {
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
	clk := clock.NewMutable(t0)
	aist := fakeaist.New(t, t0)
	journal := messaging.NewJournal("inventory", auditc.ModuleInventory, clk)
	svc := app.New(app.Deps{
		Pool: pool, Store: postgres.New(pool), Aist: aistclient.New(aist.URL, aistclient.Options{}),
		Publisher: events.New(journal), Journal: journal, Ref: ref, Clock: clk,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		// Здесь проверяются права и контракт HTTP, а не задержка БД в локальном кластере.
		Config: app.Config{DBTimeout: 2 * time.Second},
	})
	idp := authtest.New(t)
	denied := &denials{}
	mux := http.NewServeMux()
	api.Mount(mux, svc, denied.record, api.Options{DevEnabled: dev})
	srv := httptest.NewServer(httpx.Chain(mux, auth.Authenticate(idp.Verifier(t, ""))))
	t.Cleanup(srv.Close)
	return env{server: srv, idp: idp, aist: aist, svc: svc, pool: pool, clock: clk, denied: denied}
}

func (e env) token(t *testing.T, role auth.Role) string {
	t.Helper()
	return e.idp.Token(t, authtest.TokenOptions{Username: strings.ToLower(string(role)), Roles: []auth.Role{role}})
}

func (e env) do(t *testing.T, method, path, token string, body any) *http.Response {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.server.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func (e env) syncUnit(t *testing.T, code string) uuid.UUID {
	t.Helper()
	e.aist.Add(fakeaist.Unit{AistUnitCode: code})
	e.clock.Advance(5 * time.Second)
	if _, err := e.svc.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	if err := e.pool.QueryRow(context.Background(), `SELECT unit_id FROM unit WHERE aist_unit_code = $1`, code).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// decodeStrict разбирает ответ в сгенерированный тип контракта, не допуская лишних полей.
func decodeStrict(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	dec := json.NewDecoder(resp.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("ответ не соответствует контракту: %v", err)
	}
}

// Статус единиц — только служебному токену с ролью SERVICE; остальным 403 с записью отказа в аудит.
func TestUnitStatusRequiresServiceRole(t *testing.T) {
	e := setup(t, false)
	id := e.syncUnit(t, "U-1")
	path := "/internal/units/status?ids=" + id.String()

	resp := e.do(t, http.MethodGet, path, "", nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("без токена: %d, ожидали 401", resp.StatusCode)
	}
	for _, role := range []auth.Role{auth.RolePhysician, auth.RoleOperator, auth.RoleAnalyst, auth.RoleDuty, auth.RoleITService} {
		resp := e.do(t, http.MethodGet, path, e.token(t, role), nil)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("роль %s: %d, ожидали 403", role, resp.StatusCode)
		}
	}
	denied := e.denied.list()
	if len(denied) != 5 || denied[0] != auth.OpInternalUnitStatus {
		t.Fatalf("отказы в аудит: %v", denied)
	}
	resp = e.do(t, http.MethodGet, path, e.token(t, auth.RoleService), nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, serviceErr := e.svc.UnitStatuses(t.Context(), []uuid.UUID{id})
		t.Fatalf("роль SERVICE: %d, ожидали 200; ошибка сценария: %v", resp.StatusCode, serviceErr)
	}
}

// Ответ соответствует контракту OpenAPI: статус из АИСТ-М, доступность, неизвестные единицы.
func TestUnitStatusResponseMatchesContract(t *testing.T) {
	e := setup(t, false)
	id := e.syncUnit(t, "U-1")
	unknown := uuid.Must(uuid.NewV7())
	resp := e.do(t, http.MethodGet, "/internal/units/status?ids="+id.String()+","+unknown.String(), e.token(t, auth.RoleService), nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("ответ %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var rep gen.UnitStatusReport
	decodeStrict(t, resp, &rep)
	if rep.Source != "aist" || len(rep.Units) != 1 || !rep.Units[0].Available || rep.Units[0].Status != "IN_STOCK" ||
		uuid.UUID(rep.Units[0].UnitId) != id || rep.Units[0].ClinicId != 1 || rep.Watermark == nil {
		t.Fatalf("отчёт: %+v", rep)
	}
	if len(rep.NotFound) != 1 || uuid.UUID(rep.NotFound[0]) != unknown {
		t.Fatalf("not_found: %v", rep.NotFound)
	}
}

// АИСТ-М недоступна: ответ из копии с source=copy и водяным знаком.
func TestUnitStatusFallsBackToCopy(t *testing.T) {
	e := setup(t, false)
	id := e.syncUnit(t, "U-1")
	syncedAt := e.clock.Now()
	e.aist.SetDown(true)
	resp := e.do(t, http.MethodGet, "/internal/units/status?ids="+id.String(), e.token(t, auth.RoleService), nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ответ %d", resp.StatusCode)
	}
	var rep gen.UnitStatusReport
	decodeStrict(t, resp, &rep)
	if rep.Source != "copy" || rep.Watermark == nil || !rep.Watermark.Equal(syncedAt) || len(rep.Units) != 1 || !rep.Units[0].Available {
		t.Fatalf("ответ из копии: %+v", rep)
	}
}

func TestUnitStatusRejectsBadIDs(t *testing.T) {
	e := setup(t, false)
	tok := e.token(t, auth.RoleService)
	for _, q := range []string{"", "?ids=", "?ids=not-a-uuid"} {
		resp := e.do(t, http.MethodGet, "/internal/units/status"+q, tok, nil)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%q: %d, ожидали 400", q, resp.StatusCode)
		}
	}
}

// Демо-маршрут защищённого объёма вне BS_ENV=local отвечает 404 даже роли с правом.
func TestProtectedVolumeIsHiddenOutsideLocal(t *testing.T) {
	e := setup(t, false)
	body := map[string]any{"clinic_id": 1, "component_kind": "RBC", "volume_units": 3}
	resp := e.do(t, http.MethodPost, "/dev/protected-volume", e.token(t, auth.RoleDuty), body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("вне local: %d, ожидали 404", resp.StatusCode)
	}
}

func TestProtectedVolumeOnLocalStand(t *testing.T) {
	e := setup(t, true)
	body := map[string]any{"clinic_id": 1, "component_kind": "RBC", "volume_units": 3}
	denied := e.do(t, http.MethodPost, "/dev/protected-volume", e.token(t, auth.RoleService), body)
	_ = denied.Body.Close()
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("роль SERVICE: %d, ожидали 403", denied.StatusCode)
	}
	resp := e.do(t, http.MethodPost, "/dev/protected-volume", e.token(t, auth.RoleDuty), body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("дежурный: %d, ожидали 202", resp.StatusCode)
	}
	var pv gen.ProtectedVolume
	decodeStrict(t, resp, &pv)
	if pv.ClinicId != 1 || pv.ComponentKind != "RBC" || pv.VolumeUnits != 3 || pv.Version < 1 || pv.Reason == "" {
		t.Fatalf("ответ: %+v", pv)
	}
	bad := map[string]any{"clinic_id": 1, "component_kind": "RBC", "volume_units": -1}
	invalid := e.do(t, http.MethodPost, "/dev/protected-volume", e.token(t, auth.RoleDuty), bad)
	_ = invalid.Body.Close()
	if invalid.StatusCode != http.StatusBadRequest {
		t.Fatalf("отрицательный объём: %d, ожидали 400", invalid.StatusCode)
	}
}
