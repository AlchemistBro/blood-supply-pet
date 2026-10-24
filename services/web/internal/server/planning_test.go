package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/oidc/oidctest"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/svcclient"
)

func TestPlanningScreensRenderAndBuildRequiresCSRF(t *testing.T) {
	e := newEnv(t)
	id := "01920000-0000-7000-8000-000000000001"
	writes := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Error("токен пользователя не передан")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			writes++
		}
		switch r.URL.Path {
		case "/plans", "/forecasts", "/deficits":
			_, _ = io.WriteString(w, `[]`)
		case "/plans/" + id:
			_, _ = io.WriteString(w, `{"replenishment_plan_id":"`+id+`","algorithm_version":"demo-1","coverage_ratio":1,"proposals":[{"explanation":"<script>alert(1)</script>"}],"targets":[],"deficits":[],"protected":[]}`)
		case "/forecasts/" + id:
			_, _ = io.WriteString(w, `{"emergency_forecast_id":"`+id+`","scenario_version":"demo-1","is_rough_estimate":true,"lines":[{"line_id":"`+id+`","component_kind":"RBC","volume_ml":300}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	e.srv.planning = svcclient.New("planning", upstream.URL)
	mux := http.NewServeMux()
	e.srv.Register(mux)
	e.handler = httpx.Chain(mux, e.srv.Middleware()...)
	j := e.login(oidctest.User{Username: "analyst", Roles: []auth.Role{auth.RoleAnalyst}}, "/plans")
	for _, path := range []string{"/plans", "/plans/" + id, "/forecasts/" + id, "/deficits"} {
		response := e.do(http.MethodGet, path, j, nil)
		if response.Code != 200 {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "<script>alert") {
			t.Fatal("объяснение попало в HTML без экранирования")
		}
	}
	if response := e.do(http.MethodPost, "/plans", j, nil); response.Code != 403 || writes != 0 {
		t.Fatalf("POST без CSRF: %d, записей %d", response.Code, writes)
	}
}
