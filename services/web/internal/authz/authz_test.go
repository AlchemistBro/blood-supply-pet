package authz

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/auditclient"
)

type fakeAudit struct {
	mu      sync.Mutex
	entries []auditclient.Entry
	err     error
}

func (f *fakeAudit) Record(_ context.Context, e auditclient.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, e)
	return f.err
}

func forbiddenPage(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(w, "Недостаточно прав")
}

func newGuard(a auditclient.Recorder) *Guard {
	return New(a, slog.New(slog.NewTextHandler(io.Discard, nil)), http.HandlerFunc(forbiddenPage))
}

func clinic(id int64) *int64 { return &id }

var (
	operator1 = auth.Principal{Username: "operator-c001", Roles: []auth.Role{auth.RoleOperator}, ClinicID: clinic(1)}
	physician = auth.Principal{Username: "physician1", Roles: []auth.Role{auth.RolePhysician}, ClinicID: clinic(1)}
	duty      = auth.Principal{Username: "duty1", Roles: []auth.Role{auth.RoleDuty}}
)

func serve(h http.Handler, method, target string, p *auth.Principal) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	if p != nil {
		r = r.WithContext(auth.WithPrincipal(r.Context(), *p))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

func TestRequireAllowsRoleFromMatrix(t *testing.T) {
	a := &fakeAudit{}
	h := newGuard(a).Require(auth.OpConfirmTransfer)(okHandler)
	if rec := serve(h, http.MethodPost, "/tasks/1/confirm", &operator1); rec.Code != http.StatusNoContent {
		t.Fatalf("оператору подтверждение разрешено, получен %d", rec.Code)
	}
	if len(a.entries) != 0 {
		t.Fatal("разрешённый запрос не пишет отказ в аудит")
	}
}

// Чужая роль: 403 человеческим языком и запись ACCESS_DENIED / DENIED в аудит.
func TestRequireDeniesForeignRoleAndAudits(t *testing.T) {
	a := &fakeAudit{}
	called := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	h := newGuard(a).Require(auth.OpConfirmTransfer)(next)
	rec := serve(h, http.MethodPost, "/tasks/1/confirm", &physician)
	if rec.Code != http.StatusForbidden || rec.Body.String() != "Недостаточно прав" {
		t.Fatalf("врачу подтверждение запрещено: %d %q", rec.Code, rec.Body.String())
	}
	if called {
		t.Fatal("обработчик не должен вызываться при отказе")
	}
	if len(a.entries) != 1 {
		t.Fatalf("ожидалась одна запись аудита, записей %d", len(a.entries))
	}
	e := a.entries[0]
	if e.Action != auditc.ActionAccessDenied || e.Result != auditc.ResultDenied || e.ObjectType != "operation" ||
		e.ObjectID != string(auth.OpConfirmTransfer) || e.Principal == nil || e.Principal.Username != "physician1" {
		t.Fatalf("запись аудита: %+v", e)
	}
	if e.Details["reason"] != "role" || e.Details["path"] != "/tasks/1/confirm" {
		t.Fatalf("details: %+v", e.Details)
	}
}

// Недоступный аудит не превращает отказ в 500 и не открывает доступ.
func TestRequireDeniesEvenIfAuditFails(t *testing.T) {
	a := &fakeAudit{err: errors.New("audit недоступен")}
	h := newGuard(a).Require(auth.OpConfirmTransfer)(okHandler)
	if rec := serve(h, http.MethodPost, "/tasks/1/confirm", &physician); rec.Code != http.StatusForbidden {
		t.Fatalf("ожидался 403, получен %d", rec.Code)
	}
}

func TestRequireWithoutPrincipalRedirectsToLogin(t *testing.T) {
	h := newGuard(&fakeAudit{}).Require(auth.OpViewTasks)(okHandler)
	rec := serve(h, http.MethodGet, "/tasks", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("без входа ожидался переход на /login: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

func clinicFromQuery(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.URL.Query().Get("clinic"), 10, 64)
}

// Клиника объекта сверяется с clinic_id из токена; несовпадение — 403 и аудит.
func TestRequireClinic(t *testing.T) {
	a := &fakeAudit{}
	h := newGuard(a).RequireClinic(auth.OpViewTasks, clinicFromQuery)(okHandler)

	if rec := serve(h, http.MethodGet, "/tasks?clinic=1", &operator1); rec.Code != http.StatusNoContent {
		t.Fatalf("своя клиника: %d", rec.Code)
	}
	if rec := serve(h, http.MethodGet, "/tasks?clinic=2", &operator1); rec.Code != http.StatusForbidden {
		t.Fatalf("чужая клиника: ожидался 403, получен %d", rec.Code)
	}
	if len(a.entries) != 1 || a.entries[0].Details["reason"] != "clinic" {
		t.Fatalf("отказ по клинике пишется в аудит: %+v", a.entries)
	}
	// Дежурный не привязан к клинике и видит все (security.md).
	if rec := serve(h, http.MethodGet, "/tasks?clinic=2", &duty); rec.Code != http.StatusNoContent {
		t.Fatalf("дежурному доступны все клиники: %d", rec.Code)
	}
	// Роль без права на операцию отсекается раньше клиники.
	if rec := serve(h, http.MethodGet, "/tasks?clinic=1", &physician); rec.Code != http.StatusForbidden {
		t.Fatalf("врачу задания недоступны: %d", rec.Code)
	}
	// Клиника не разобрана — отказ, а не доступ.
	if rec := serve(h, http.MethodGet, "/tasks?clinic=abc", &operator1); rec.Code != http.StatusForbidden {
		t.Fatalf("неразобранная клиника: ожидался 403, получен %d", rec.Code)
	}
}

func TestClinicFromContext(t *testing.T) {
	ctx := auth.WithPrincipal(context.Background(), operator1)
	if id, ok := ClinicID(ctx); !ok || id != 1 {
		t.Fatalf("clinic_id из токена: %d %v", id, ok)
	}
	if _, ok := ClinicID(auth.WithPrincipal(context.Background(), duty)); ok {
		t.Fatal("у дежурного клиники нет")
	}
	if _, ok := ClinicID(context.Background()); ok {
		t.Fatal("без участника клиники нет")
	}
}
