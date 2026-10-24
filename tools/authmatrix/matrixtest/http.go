// Package matrixtest проверяет реальный HTTP-отказ по контракту, без БД и бизнес-обработчиков.
package matrixtest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth/authtest"
	"github.com/AlchemistBro/blood-supply-pet/tools/authmatrix/matrix"
)

// CheckDenials проверяет 401 без токена и 403 с аудитом для каждой запрещённой роли из OpenAPI.
// Допущенные роли проверяются сценариями сервиса с его бизнес-данными.
func CheckDenials(t *testing.T, service string, spec []byte, mount func(*http.ServeMux, auth.Denied)) {
	t.Helper()
	eps, err := matrix.ParseSpec(service, spec)
	if err != nil {
		t.Fatal(err)
	}
	idp := authtest.New(t)
	mux := http.NewServeMux()
	var deniedOp auth.Operation
	var deniedRole auth.Role
	count := 0
	mount(mux, func(_ *http.Request, p *auth.Principal, op auth.Operation) {
		count++
		deniedOp, deniedRole = op, p.Roles[0]
	})
	handler := auth.Authenticate(idp.Verifier(t, "blood-supply-api"))(mux)
	tokens := make(map[auth.Role]string)
	for _, role := range matrix.Roles() {
		tokens[role] = idp.Token(t, authtest.TokenOptions{Username: "matrix", Roles: []auth.Role{role}, Audience: "blood-supply-api", ClinicID: authtest.Clinic(1)})
	}
	request := func(method, path, token string) *httptest.ResponseRecorder {
		path = matrix.FillPath(path, func(string) string {
			if strings.HasPrefix(path, "/clinics/") {
				return "1"
			}
			return "01920000-0000-7000-8000-000000000001"
		})
		path += "?ids=01920000-0000-7000-8000-000000000001&sourceClinicId=1"
		r := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	checked := 0
	for _, e := range eps {
		if e.Public {
			continue
		}
		for _, token := range []string{"", "invalid.token.signature"} {
			t.Run(e.Method+" "+e.Path+" unauthorized "+token, func(t *testing.T) {
				if w := request(e.Method, e.Path, token); w.Code != http.StatusUnauthorized {
					t.Fatalf("ожидался 401, получен %d", w.Code)
				}
			})
		}
	}
	for _, c := range matrix.BuildCases(eps) {
		if !c.WantForbidden {
			continue
		}
		checked++
		t.Run(c.Name(), func(t *testing.T) {
			before := count
			w := request(c.Method, c.Path, tokens[c.Role])
			if w.Code != http.StatusForbidden || w.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("ожидался problem+json 403, получен %d: %s", w.Code, w.Body.String())
			}
			if count != before+1 || deniedOp != c.Operation || deniedRole != c.Role {
				t.Fatalf("нет точного аудита отказа: count=%d operation=%s role=%s", count-before, deniedOp, deniedRole)
			}
		})
	}
	if checked == 0 {
		t.Fatal("ни один отказ не проверен")
	}
}
