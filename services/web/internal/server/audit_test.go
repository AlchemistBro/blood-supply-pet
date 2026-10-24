package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/oidc/oidctest"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/svcclient"
)

func TestAuditScreenRequiresRoleAndCSRFForVerification(t *testing.T) {
	e := newEnv(t)
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/internal/verify" {
			_, _ = io.WriteString(w, `{"ok":false,"checked":3,"broken":{"seq":4,"reason":"hash"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"items":[{"seq":1,"occurred_at":"2026-10-24T10:00:00Z","module":"MATCHING","action":"READ_PATIENT_CASE","object_type":"clinical_request","object_id":"demo","result":"SUCCESS","user_id":"doctor","hash":"abc"}]}`)
	}))
	defer upstream.Close()
	e.srv.auditReader = svcclient.New("audit", upstream.URL)
	mux := http.NewServeMux()
	e.srv.Register(mux)
	e.handler = httpx.Chain(mux, e.srv.Middleware()...)
	j := e.login(physician, "/audit")
	response := e.do(http.MethodGet, "/audit", j, nil)
	if response.Code != http.StatusForbidden || calls != 0 {
		t.Fatalf("журнал доступен врачу: %d, %d", response.Code, calls)
	}
	response = e.do(http.MethodPost, "/audit/verify", j, nil)
	if response.Code != http.StatusForbidden || calls != 0 {
		t.Fatal("проверка цепочки вызвана без CSRF")
	}
	if strings.Contains(response.Body.String(), "READ_PATIENT_CASE") {
		t.Fatal("запись раскрыта в отказе")
	}
	j = e.login(oidctest.User{Username: "it-service", Roles: []auth.Role{auth.RoleITService}}, "/audit")
	response = e.do(http.MethodGet, "/audit?module=MATCHING", j, nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "READ_PATIENT_CASE") {
		t.Fatalf("журнал ИТ-службы: %d %s", response.Code, response.Body.String())
	}
	response = e.do(http.MethodPost, "/audit/verify", j, postForm(url.Values{"csrf_token": {csrfOf(t, e, j)}}))
	if response.Code != 200 || !strings.Contains(response.Body.String(), "Запись № 4") {
		t.Fatalf("не показан разрыв: %d %s", response.Code, response.Body.String())
	}
	response = e.do(http.MethodGet, "/audit/export.csv", j, nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "READ_PATIENT_CASE") || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("выгрузка журнала недоступна")
	}
}

func TestAuditCSVNeutralizesSpreadsheetFormulas(t *testing.T) {
	for _, value := range []string{"=HYPERLINK(1)", "+cmd", "-cmd", "@sum(1)", "\t=1"} {
		if got := csvCell(value); got != "'"+value {
			t.Fatalf("формула не нейтрализована: %q", got)
		}
	}
}
