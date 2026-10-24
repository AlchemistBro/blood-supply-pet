package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/app"
)

type verifier struct{}

func (verifier) Verify(_ context.Context, raw string) (auth.Principal, error) {
	return auth.Principal{Subject: "demo", Roles: []auth.Role{auth.Role(raw)}}, nil
}
func TestTransportStatusIsLimitedToDutyAndIT(t *testing.T) {
	calls := 0
	mux := http.NewServeMux()
	api.Mount(mux, func(context.Context) ([]app.TransportStatus, error) { calls++; return []app.TransportStatus{}, nil }, nil)
	handler := auth.Authenticate(verifier{})(mux)
	for _, role := range []auth.Role{auth.RolePhysician, auth.RoleOperator, auth.RoleAnalyst, auth.RoleService, auth.RoleDuty, auth.RoleITService} {
		before := calls
		r := httptest.NewRequest(http.MethodGet, "/internal/transport-status", nil)
		r.Header.Set("Authorization", "Bearer "+string(role))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if role == auth.RoleDuty || role == auth.RoleITService {
			if w.Code != 200 || calls != before+1 {
				t.Fatalf("%s: %d", role, w.Code)
			}
		} else if w.Code != 403 || calls != before {
			t.Fatalf("%s получил состояние шлюза: %d", role, w.Code)
		}
	}
}
