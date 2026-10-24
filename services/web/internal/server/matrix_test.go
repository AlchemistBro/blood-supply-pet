package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	apispec "github.com/AlchemistBro/blood-supply-pet/services/web/api"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/live"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/oidc/oidctest"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/svcclient"
	"github.com/AlchemistBro/blood-supply-pet/tools/authmatrix/matrix"
)

func TestOpenAPIRejectsEveryUnauthorizedRole(t *testing.T) {
	e := newEnv(t)
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(upstream.Close)
	client := svcclient.New("matrix", upstream.URL)
	e.srv.transfer, e.srv.inventory, e.srv.matching, e.srv.demand = client, client, client, client
	e.srv.planning, e.srv.auditReader, e.srv.logistics, e.srv.notification = client, client, client, client
	mux := http.NewServeMux()
	e.srv.Register(mux)
	mux.Handle("GET /events/obligations", live.NewSSE(live.NewHub(1), e.srv.Guard(), e.srv.log, live.SSEConfig{}).Handler())
	e.handler = httpx.Chain(mux, e.srv.Middleware()...)
	cases, err := matrix.CasesFromSpec("web", apispec.Spec)
	if err != nil {
		t.Fatal(err)
	}
	sessions := make(map[auth.Role]jar)
	for _, role := range matrix.Roles() {
		sessions[role] = e.login(oidctest.User{Username: "matrix-" + string(role), Roles: []auth.Role{role}, ClinicID: clinicPtr(1)}, "/")
	}
	for _, c := range cases {
		if !c.WantForbidden {
			continue
		}
		t.Run(c.Name(), func(t *testing.T) {
			path := matrix.FillPath(c.Path, func(string) string { return taskID })
			before := len(e.audit.entries)
			j := sessions[c.Role]
			rec := e.do(c.Method, path, j, postForm(url.Values{"csrf_token": {csrfOf(t, e, j)}}))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("ожидался 403, получен %d", rec.Code)
			}
			denied := 0
			for _, entry := range e.audit.entries[before:] {
				if entry.Action != auditc.ActionAccessDenied {
					continue
				}
				denied++
				if entry.ObjectID != string(c.Operation) || entry.Result != auditc.ResultDenied {
					t.Fatalf("неверный аудит отказа: %+v", entry)
				}
			}
			if denied != 1 {
				t.Fatalf("записей ACCESS_DENIED: %d, ожидалась одна", denied)
			}
		})
	}
	if upstreamCalls != 0 {
		t.Fatalf("запрещённые запросы дошли до сервисов: %d", upstreamCalls)
	}
}

func TestCSRFRejectsTokenFromAnotherSessionAndQuery(t *testing.T) {
	e := newEnv(t)
	first := e.login(operator, "/")
	second := e.login(operator, "/")
	foreignToken := csrfOf(t, e, first)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			rec := e.do(method, "/tasks/1/confirm", second, postForm(url.Values{"csrf_token": {foreignToken}}))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("токен другой сессии: %d", rec.Code)
			}
		})
	}
	rec := e.do(http.MethodPost, "/tasks/1/confirm?csrf_token="+url.QueryEscape(csrfOf(t, e, second)), second, postForm(url.Values{}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("токен из query принят: %d", rec.Code)
	}
}
