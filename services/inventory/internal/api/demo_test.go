package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
)

type demoVerifier struct{}

func (demoVerifier) Verify(_ context.Context, raw string) (auth.Principal, error) {
	return auth.Principal{Subject: "demo", Roles: []auth.Role{auth.Role(raw)}}, nil
}
func TestDemoSeedIsHiddenOutsideDemoAndLimitedToIT(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, role := range []auth.Role{auth.RoleITService, auth.RolePhysician, auth.RoleOperator, auth.RoleAnalyst, auth.RoleDuty, auth.RoleService} {
			called := false
			mux := http.NewServeMux()
			mux.Handle("POST /dev/seed-norms", authorize(nil, Options{SeedEnabled: enabled})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(204) })))
			r := httptest.NewRequest(http.MethodPost, "/dev/seed-norms", nil)
			r.Header.Set("Authorization", "Bearer "+string(role))
			w := httptest.NewRecorder()
			auth.Authenticate(demoVerifier{})(mux).ServeHTTP(w, r)
			want := 403
			if !enabled {
				want = 404
			} else if role == auth.RoleITService {
				want = 204
			}
			if w.Code != want || called != (want == 204) {
				t.Fatalf("enabled=%v role=%s: %d, called=%v", enabled, role, w.Code, called)
			}
		}
	}
}
