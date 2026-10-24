package auth_test

import (
	"context"
	"errors"
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
		TokenURL:       auth.TokenURL(idp.Issuer),
		ClientID:       "services",
		ClientSecret:   secret,
		RefreshBefore:  30 * time.Second,
		RefreshTimeout: time.Second,
		Clock:          clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// waitIssued ждёт, пока фоновое обновление дойдёт до IdP: обновление в окне не блокирует вызывающего.
func waitIssued(t *testing.T, idp *authtest.IdP, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for idp.IssuedTokens("services") < want {
		if time.Now().After(deadline) {
			t.Fatalf("выдано токенов %d, ожидали %d", idp.IssuedTokens("services"), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
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

// Токен кэшируется до окна обновления; в окне отдаётся прежний, а новый запрашивается в фоне.
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
	time.Sleep(50 * time.Millisecond)
	if second != first || idp.IssuedTokens("services") != 1 {
		t.Fatalf("до окна обновления токен должен браться из кэша: выдано %d", idp.IssuedTokens("services"))
	}
	clk.Advance(2 * time.Second) // осталось меньше 30 с до истечения
	inWindow, err := st.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if inWindow != first {
		t.Fatal("в окне обновления должен сразу отдаваться действующий токен")
	}
	waitIssued(t, idp, 2)
}

// Зависший Keycloak не задерживает вызов, у которого есть действующий токен; вызов без токена ждёт
// не дольше своего контекста.
func TestServiceTokenDoesNotWaitForHangingIdP(t *testing.T) {
	idp := authtest.New(t)
	idp.RegisterClient("services", servicesSecret, []auth.Role{auth.RoleService}, 5*time.Minute)
	clk := clock.NewMutable(time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC))
	st := newServiceToken(t, idp, servicesSecret, clk)

	first, err := st.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release := idp.HoldTokenRequests()
	t.Cleanup(release)

	clk.Advance(4*time.Minute + 45*time.Second) // окно обновления
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	got, err := st.Token(ctx)
	if err != nil || got != first {
		t.Fatalf("ожидали действующий токен без ожидания: err=%v", err)
	}
	if waited := time.Since(start); waited > 200*time.Millisecond {
		t.Fatalf("вызов с действующим токеном ждал зависший IdP %v", waited)
	}

	// Пустой кэш у другого источника: ждём не дольше своего контекста.
	cold := newServiceToken(t, idp, servicesSecret, clock.NewMutable(time.Now()))
	short, cancelShort := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelShort()
	start = time.Now()
	if _, err := cold.Token(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ожидали истечение контекста вызывающего, получили %v", err)
	}
	if waited := time.Since(start); waited > 500*time.Millisecond {
		t.Fatalf("вызов ждал дольше своего контекста: %v", waited)
	}
}

// Сбой Keycloak при обновлении не роняет вызовы, пока прежний токен ещё действует; после истечения — ошибка;
// после сбоя следующая попытка не раньше паузы.
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
	time.Sleep(100 * time.Millisecond) // фоновая попытка успевает завершиться сбоем
	clk.Advance(time.Minute)           // токен истёк, пауза после сбоя прошла
	if _, err := st.Token(ctx); err == nil {
		t.Fatal("после истечения срока без обновления ожидали ошибку")
	}
	idp.FailTokenRequests(false)
	if _, err := st.Token(ctx); err == nil {
		t.Fatal("сразу после сбоя ожидали паузу, а не новый запрос")
	}
	clk.Advance(time.Minute)
	if _, err := st.Token(ctx); err != nil {
		t.Fatalf("после паузы и восстановления Keycloak токен должен получаться: %v", err)
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
