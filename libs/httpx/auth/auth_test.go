package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth/authtest"
)

func TestPolicyCoversEveryOperation(t *testing.T) {
	for _, op := range auth.Operations() {
		if len(auth.RolesFor(op)) == 0 {
			t.Errorf("операция %s без ролей", op)
		}
	}
	if auth.Allowed(auth.OpConfirmTransfer, []auth.Role{auth.RoleAnalyst}) {
		t.Error("аналитик не должен подтверждать передачу")
	}
	if !auth.Allowed(auth.OpConfirmTransfer, []auth.Role{auth.RoleOperator}) {
		t.Error("оператор должен подтверждать передачу")
	}
	if auth.Allowed(auth.Operation("UNKNOWN"), []auth.Role{auth.RoleITService}) {
		t.Error("неизвестная операция должна быть запрещена всем")
	}
}

func TestVerifyAndRequire(t *testing.T) {
	idp := authtest.New(t)
	verifier := idp.Verifier(t, "blood-supply-api")

	denied := 0
	handler := httpx.Chain(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, _ := auth.FromContext(r.Context())
			if p.Username != "operator-c001" || p.ClinicID == nil || *p.ClinicID != 1 {
				t.Errorf("неверный участник: %+v", p)
			}
			w.WriteHeader(http.StatusNoContent)
		}),
		auth.Authenticate(verifier),
		auth.Require(auth.OpConfirmTransfer, func(*http.Request, *auth.Principal, auth.Operation) { denied++ }),
	)

	call := func(token string) int {
		req := httptest.NewRequest(http.MethodPost, "/obligations/x/confirm", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	operator := idp.Token(t, authtest.TokenOptions{Username: "operator-c001", Roles: []auth.Role{auth.RoleOperator}, ClinicID: authtest.Clinic(1), Audience: "blood-supply-api"})
	analyst := idp.Token(t, authtest.TokenOptions{Username: "analyst1", Roles: []auth.Role{auth.RoleAnalyst}, Audience: "blood-supply-api"})
	expired := idp.Token(t, authtest.TokenOptions{Username: "operator-c001", Roles: []auth.Role{auth.RoleOperator}, Audience: "blood-supply-api", TTL: -time.Minute})
	wrongAud := idp.Token(t, authtest.TokenOptions{Username: "operator-c001", Roles: []auth.Role{auth.RoleOperator}, Audience: "other"})

	if got := call(""); got != http.StatusUnauthorized {
		t.Errorf("без токена: %d", got)
	}
	if got := call(expired); got != http.StatusUnauthorized {
		t.Errorf("просроченный: %d", got)
	}
	if got := call(wrongAud); got != http.StatusUnauthorized {
		t.Errorf("чужая аудитория: %d", got)
	}
	if got := call(analyst); got != http.StatusForbidden {
		t.Errorf("аналитик: %d", got)
	}
	if denied != 1 {
		t.Errorf("onDenied вызван %d раз", denied)
	}
	if got := call(operator); got != http.StatusNoContent {
		t.Errorf("оператор: %d", got)
	}
}

func TestVerifierRejectsForeignIssuer(t *testing.T) {
	idp := authtest.New(t)
	other := authtest.New(t)
	v := idp.Verifier(t, "")
	tok := other.Token(t, authtest.TokenOptions{Username: "x", Roles: []auth.Role{auth.RoleDuty}})
	if _, err := v.Verify(context.Background(), tok); err == nil {
		t.Fatal("токен чужого издателя принят")
	}
}
