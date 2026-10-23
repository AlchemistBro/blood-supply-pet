package auth_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth/authtest"
)

const servicesSecret = "services-test-secret"

func newServiceToken(t *testing.T, idp *authtest.IdP, secret string, clk clock.Clock) *auth.ServiceToken {
	t.Helper()
	st, err := auth.NewServiceToken(auth.ClientCredentialsConfig{
		TokenURL:      auth.TokenURL(idp.Issuer),
		ClientID:      "services",
		ClientSecret:  secret,
		RefreshBefore: 30 * time.Second,
		Clock:         clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// Служебный токен проходит проверку подписи и даёт роль SERVICE: с ним transfer вызывает
// GET /internal/units/status у inventory (ADR-0004).
func TestServiceTokenIsAcceptedWithServiceRole(t *testing.T) {
	idp := authtest.New(t)
	idp.RegisterClient("services", servicesSecret, []auth.Role{auth.RoleService}, 5*time.Minute)
	st := newServiceToken(t, idp, servicesSecret, clock.NewMutable(time.Now()))

	raw, err := st.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p, err := idp.Verifier(t, "blood-supply-api").Verify(context.Background(), raw)
	if err != nil {
		t.Fatalf("токен не прошёл проверку: %v", err)
	}
	if !p.HasRole(auth.RoleService) || !auth.Allowed(auth.OpInternalUnitStatus, p.Roles) {
		t.Fatalf("у служебного токена нет права на статус единиц: %+v", p)
	}
}

// Токен кэшируется до окна обновления и обновляется заранее, а не после истечения.
func TestServiceTokenCachesAndRefreshesAhead(t *testing.T) {
	idp := authtest.New(t)
	idp.RegisterClient("services", servicesSecret, []auth.Role{auth.RoleService}, 5*time.Minute)
	clk := clock.NewMutable(time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC))
	st := newServiceToken(t, idp, servicesSecret, clk)
	ctx := context.Background()

	first, err := st.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(4*time.Minute + 29*time.Second)
	second, err := st.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second != first || idp.IssuedTokens("services") != 1 {
		t.Fatalf("до окна обновления токен должен браться из кэша: выдано %d", idp.IssuedTokens("services"))
	}
	clk.Advance(2 * time.Second) // осталось меньше 30 с до истечения
	if _, err := st.Token(ctx); err != nil {
		t.Fatal(err)
	}
	if idp.IssuedTokens("services") != 2 {
		t.Fatalf("в окне обновления токен должен быть получен заранее: выдано %d", idp.IssuedTokens("services"))
	}
}

// Сбой Keycloak при обновлении не роняет вызовы, пока прежний токен ещё действует; после истечения — ошибка.
func TestServiceTokenSurvivesRefreshFailureUntilExpiry(t *testing.T) {
	idp := authtest.New(t)
	idp.RegisterClient("services", servicesSecret, []auth.Role{auth.RoleService}, 5*time.Minute)
	clk := clock.NewMutable(time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC))
	st := newServiceToken(t, idp, servicesSecret, clk)
	ctx := context.Background()

	first, err := st.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	idp.FailTokenRequests(true)
	clk.Advance(4*time.Minute + 45*time.Second)
	got, err := st.Token(ctx)
	if err != nil || got != first {
		t.Fatalf("при сбое обновления ожидали прежний токен: err=%v", err)
	}
	clk.Advance(time.Minute)
	if _, err := st.Token(ctx); err == nil {
		t.Fatal("после истечения срока без обновления ожидали ошибку")
	}
	idp.FailTokenRequests(false)
	if _, err := st.Token(ctx); err != nil {
		t.Fatalf("после восстановления Keycloak токен должен получаться: %v", err)
	}
}

func TestServiceTokenRejectsWrongSecret(t *testing.T) {
	idp := authtest.New(t)
	idp.RegisterClient("services", servicesSecret, []auth.Role{auth.RoleService}, 5*time.Minute)
	st := newServiceToken(t, idp, "wrong", clock.NewMutable(time.Now()))
	if tok, err := st.Token(context.Background()); err == nil || tok != "" {
		t.Fatalf("неверный секрет должен давать ошибку: %q %v", tok, err)
	}
}

// Одновременные вызовы при пустом кэше получают один токен одним запросом.
func TestServiceTokenSingleFetchUnderConcurrency(t *testing.T) {
	idp := authtest.New(t)
	idp.RegisterClient("services", servicesSecret, []auth.Role{auth.RoleService}, 5*time.Minute)
	st := newServiceToken(t, idp, servicesSecret, clock.NewMutable(time.Now()))
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := st.Token(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := idp.IssuedTokens("services"); n != 1 {
		t.Fatalf("выдано токенов %d, ожидали 1", n)
	}
}

func TestServiceTokenAuthorizeSetsBearer(t *testing.T) {
	idp := authtest.New(t)
	idp.RegisterClient("services", servicesSecret, []auth.Role{auth.RoleService}, 5*time.Minute)
	st := newServiceToken(t, idp, servicesSecret, clock.NewMutable(time.Now()))
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://inventory/internal/units/status", nil)
	if err := st.Authorize(req); err != nil {
		t.Fatal(err)
	}
	if auth.BearerToken(req) == "" {
		t.Fatalf("заголовок Authorization не установлен: %q", req.Header.Get("Authorization"))
	}
}

func TestNewServiceTokenValidatesConfig(t *testing.T) {
	cases := map[string]auth.ClientCredentialsConfig{
		"нет адреса":  {ClientID: "services", ClientSecret: "s"},
		"нет клиента": {TokenURL: "http://kc/token", ClientSecret: "s"},
		"нет секрета": {TokenURL: "http://kc/token", ClientID: "services"},
	}
	for name, cfg := range cases {
		if _, err := auth.NewServiceToken(cfg); err == nil {
			t.Errorf("%s: ожидали ошибку", name)
		}
	}
}
