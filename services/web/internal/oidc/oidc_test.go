package oidc

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/oidc/oidctest"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/session"
)

const redirect = "http://localhost:8080/auth/callback"

func newClient(t *testing.T, p *oidctest.Provider) *Client {
	t.Helper()
	c, err := New(context.Background(), Config{
		Issuer: p.Issuer(), DiscoveryURL: p.URL,
		ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret,
		RedirectURL: redirect, PostLogoutRedirectURL: "http://localhost:8080/login",
		Audience: oidctest.Audience,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func loginState(t *testing.T) session.LoginState {
	t.Helper()
	st, err := NewLoginState("/tasks")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// Полный поток: authorization code + PKCE S256, nonce в id_token, роли и клиника из access token.
func TestAuthorizationCodeFlowWithPKCE(t *testing.T) {
	p := oidctest.New(t)
	c := newClient(t, p)
	st := loginState(t)

	authURL := c.AuthURL(st, "operator-c001")
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("code_challenge") == st.Verifier {
		t.Fatalf("PKCE S256 не передан: %s", authURL)
	}
	if q.Get("state") != st.State || q.Get("nonce") != st.Nonce || q.Get("login_hint") != "operator-c001" {
		t.Fatalf("state, nonce или login_hint не переданы: %s", authURL)
	}
	if !strings.Contains(q.Get("scope"), "openid") {
		t.Fatalf("нет scope openid: %s", authURL)
	}

	cb := p.Authorize(t, authURL)
	if cb.Query().Get("state") != st.State {
		t.Fatal("провайдер вернул другой state")
	}
	res, err := c.Exchange(context.Background(), cb.Query().Get("code"), st)
	if err != nil {
		t.Fatal(err)
	}
	if res.Principal.Username != "operator-c001" || !res.Principal.HasRole(auth.RoleOperator) ||
		res.Principal.ClinicID == nil || *res.Principal.ClinicID != 1 {
		t.Fatalf("участник из токена: %+v", res.Principal)
	}
	if res.AccessToken == "" || res.RefreshToken == "" || !res.Expiry.After(time.Now()) {
		t.Fatalf("токены не получены: %+v", res)
	}
	if p.LastLoginHint() != "operator-c001" {
		t.Fatal("login_hint не дошёл до провайдера")
	}
}

// id_token с чужим nonce отклоняется (повтор ответа провайдера).
func TestExchangeRejectsWrongNonce(t *testing.T) {
	p := oidctest.New(t)
	c := newClient(t, p)
	st := loginState(t)
	p.ForceNonce("чужой-nonce")
	cb := p.Authorize(t, c.AuthURL(st, ""))
	if _, err := c.Exchange(context.Background(), cb.Query().Get("code"), st); !errors.Is(err, ErrNonce) {
		t.Fatalf("ожидалась ErrNonce, получено %v", err)
	}
}

// Код с чужим PKCE verifier провайдер не обменивает.
func TestExchangeRejectsWrongVerifier(t *testing.T) {
	p := oidctest.New(t)
	c := newClient(t, p)
	st := loginState(t)
	cb := p.Authorize(t, c.AuthURL(st, ""))
	st.Verifier = oauth2.GenerateVerifier()
	if _, err := c.Exchange(context.Background(), cb.Query().Get("code"), st); err == nil {
		t.Fatal("обмен кода с чужим verifier должен провалиться")
	}
}

// Обновление по refresh token: новый access token и новый refresh token; отозванный refresh — ошибка.
func TestRefresh(t *testing.T) {
	p := oidctest.New(t)
	c := newClient(t, p)
	st := loginState(t)
	cb := p.Authorize(t, c.AuthURL(st, ""))
	first, err := c.Exchange(context.Background(), cb.Query().Get("code"), st)
	if err != nil {
		t.Fatal(err)
	}
	next, err := c.Refresh(context.Background(), first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if next.AccessToken == "" || next.RefreshToken == "" || next.RefreshToken == first.RefreshToken {
		t.Fatalf("refresh token должен ротироваться: %+v", next)
	}
	if next.Principal.Username != "operator-c001" {
		t.Fatalf("участник после обновления: %+v", next.Principal)
	}
	p.RevokeRefreshTokens()
	if _, err := c.Refresh(context.Background(), next.RefreshToken); err == nil {
		t.Fatal("отозванный refresh token не должен обновляться")
	}
}

// Discovery по внутреннему адресу при публичном issuer (compose: keycloak:8080 против localhost:8180).
func TestDiscoveryByInternalURL(t *testing.T) {
	p := oidctest.New(t)
	p.UseIssuer("http://localhost:8180/realms/blood-supply")
	c := newClient(t, p)
	st := loginState(t)
	cb := p.Authorize(t, c.AuthURL(st, ""))
	if _, err := c.Exchange(context.Background(), cb.Query().Get("code"), st); err != nil {
		t.Fatalf("токены с публичным issuer должны приниматься: %v", err)
	}
}

// Токены чужого issuer не принимаются.
func TestExchangeRejectsForeignIssuer(t *testing.T) {
	p := oidctest.New(t)
	c, err := New(context.Background(), Config{
		Issuer: "http://other.example/realms/x", DiscoveryURL: p.URL,
		ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret, RedirectURL: redirect,
	})
	if err != nil {
		t.Fatal(err)
	}
	st := loginState(t)
	cb := p.Authorize(t, c.AuthURL(st, ""))
	if _, err := c.Exchange(context.Background(), cb.Query().Get("code"), st); err == nil {
		t.Fatal("id_token чужого issuer должен отклоняться")
	}
}

func TestLogoutURL(t *testing.T) {
	p := oidctest.New(t)
	c := newClient(t, p)
	u, err := url.Parse(c.LogoutURL())
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/logout" || u.Query().Get("client_id") != oidctest.ClientID ||
		u.Query().Get("post_logout_redirect_uri") != "http://localhost:8080/login" {
		t.Fatalf("адрес выхода: %s", c.LogoutURL())
	}
	if c.ProviderOrigin() != p.URL {
		t.Fatalf("источник провайдера для CSP: %s", c.ProviderOrigin())
	}
}

func TestNewLoginStateIsRandom(t *testing.T) {
	a, b := loginState(t), loginState(t)
	if a.State == b.State || a.Nonce == b.Nonce || a.Verifier == b.Verifier || a.State == a.Nonce {
		t.Fatal("state, nonce и verifier должны быть случайными и разными")
	}
	if a.Next != "/tasks" {
		t.Fatal("адрес возврата не сохранён")
	}
}
