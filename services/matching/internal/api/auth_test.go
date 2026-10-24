package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth/authtest"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/api"
)

func TestMatchingDeniesEveryRoleWithoutPermission(t *testing.T) {
	idp := authtest.New(t)
	mux := http.NewServeMux()
	denials := 0
	api.Mount(mux, nil, func(*http.Request, *auth.Principal, auth.Operation) { denials++ })
	handler := auth.Authenticate(idp.Verifier(t, ""))(mux)
	for pattern, operation := range api.Operations {
		for _, role := range []auth.Role{auth.RoleOperator, auth.RolePhysician, auth.RoleAnalyst, auth.RoleDuty, auth.RoleITService, auth.RoleService} {
			if auth.Allowed(operation, []auth.Role{role}) {
				continue
			}
			method, path, _ := strings.Cut(pattern, " ")
			path = strings.ReplaceAll(path, "{id}", uuid.NewString())
			path = strings.ReplaceAll(path, "{code}", "DEMO_RBC_IDENTICAL")
			r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+idp.Token(t, authtest.TokenOptions{Username: "user", Roles: []auth.Role{role}}))
			w := httptest.NewRecorder()
			before := denials
			handler.ServeHTTP(w, r)
			if w.Code != 403 || denials != before+1 {
				t.Fatalf("%s %s: %d, аудит %d", pattern, role, w.Code, denials-before)
			}
		}
	}
}
