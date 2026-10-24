package api_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth/authtest"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/api"
)

var allRoles = []auth.Role{auth.RolePhysician, auth.RoleOperator, auth.RoleAnalyst, auth.RoleDuty, auth.RoleITService, auth.RoleService}

// deniedLog — что попало в аудит отказов (в сервисе это rt.Deny: ACCESS_DENIED в audit_local).
type deniedLog struct {
	mu  sync.Mutex
	ops []auth.Operation
}

func (d *deniedLog) deny(_ *http.Request, _ *auth.Principal, op auth.Operation) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ops = append(d.ops, op)
}

// requestFor строит запрос по шаблону маршрута: {id} — новый uuid, тело — пустой объект.
func requestFor(pattern string) *http.Request {
	method, path, _ := strings.Cut(pattern, " ")
	path = strings.ReplaceAll(path, "{id}", uuid.NewString())
	if method == http.MethodGet && path == "/obligations" {
		path += "?sourceClinicId=1"
	}
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(`{}`)
	}
	r := httptest.NewRequest(method, path, body)
	r.Header.Set("Content-Type", "application/json")
	return r
}

// Каждая роль без права на операцию получает 403 на каждом эндпоинте, и отказ пишется в аудит.
// Обработчик при этом не вызывается: сервис здесь не нужен.
func TestEveryRoleWithoutRightGets403(t *testing.T) {
	idp := authtest.New(t)
	denied := &deniedLog{}
	mux := http.NewServeMux()
	api.Mount(mux, nil, refdata.MustLoad(), slog.New(slog.NewTextHandler(io.Discard, nil)), denied.deny)
	handler := auth.Authenticate(idp.Verifier(t, ""))(mux)

	checked := 0
	for pattern, op := range api.Operations {
		allowed := auth.RolesFor(op)
		for _, role := range allRoles {
			if slices.Contains(allowed, role) {
				continue
			}
			token := idp.Token(t, authtest.TokenOptions{Username: "u-" + string(role), Roles: []auth.Role{role}, ClinicID: authtest.Clinic(1)})
			r := requestFor(pattern)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s роль %s: код %d, ожидали 403", pattern, role, w.Code)
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("%s роль %s: Content-Type %q", pattern, role, ct)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("ни одной пары «роль без права × эндпоинт» не проверено")
	}
	if len(denied.ops) != checked {
		t.Fatalf("в аудит отказов попало %d записей, ожидали %d", len(denied.ops), checked)
	}
}

func TestRequestWithoutTokenGets401(t *testing.T) {
	idp := authtest.New(t)
	mux := http.NewServeMux()
	api.Mount(mux, nil, refdata.MustLoad(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	handler := auth.Authenticate(idp.Verifier(t, ""))(mux)
	for pattern := range api.Operations {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, requestFor(pattern))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s без токена: %d", pattern, w.Code)
		}
	}
}
