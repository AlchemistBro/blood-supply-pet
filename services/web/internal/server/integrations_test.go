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

func TestIntegrationsShowPartialFailureWithoutGrantingPhysicianAccess(t *testing.T) {
	e := newEnv(t)
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") == "" {
			t.Error("нет токена пользователя")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/internal/sync-status":
			_, _ = io.WriteString(w, `{"is_stale":true,"watermark":"2026-10-24T10:00:00Z"}`)
		case "/internal/transport-status":
			w.WriteHeader(503)
			_, _ = io.WriteString(w, `{"title":"offline","status":503}`)
		default:
			_, _ = io.WriteString(w, `{"items":[{"notification_id":"`+taskID+`","kind":"ESCALATION","recipient_role":"DUTY_OFFICER","status":"FAILED","attempts":12,"last_error":"SMTP недоступен"}]}`)
		}
	}))
	defer upstream.Close()
	e.srv.inventory = svcclient.New("inventory", upstream.URL)
	e.srv.logistics = svcclient.New("logistics", upstream.URL)
	e.srv.notification = svcclient.New("notification", upstream.URL)
	mux := http.NewServeMux()
	e.srv.Register(mux)
	e.handler = httpx.Chain(mux, e.srv.Middleware()...)
	j := e.login(physician, "/integrations")
	response := e.do(http.MethodGet, "/integrations", j, nil)
	if response.Code != 403 || calls != 0 {
		t.Fatal("состояние интеграций раскрыто врачу")
	}
	j = e.login(oidctest.User{Username: "it-service", Roles: []auth.Role{auth.RoleITService}}, "/integrations")
	response = e.do(http.MethodGet, "/integrations", j, nil)
	for _, want := range []string{"Недоступно: C03", "Копия устарела", "SMTP недоступен"} {
		if response.Code != 200 || !strings.Contains(response.Body.String(), want) {
			t.Fatalf("нет состояния %q: %d %s", want, response.Code, response.Body.String())
		}
	}
}
