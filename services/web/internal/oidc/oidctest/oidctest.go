// Package oidctest — фальшивый OpenID-провайдер для тестов web: discovery, JWKS, authorization endpoint
// с проверкой PKCE S256, эндпоинт токенов (authorization_code, refresh_token) и end-session. Выпускает
// id_token (aud = клиент, nonce) и access token в формате Keycloak (realm_access.roles, clinic_id).
package oidctest

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
)

// Клиент и аудитория по умолчанию.
const (
	ClientID     = "web"
	ClientSecret = "web-test-secret"
	Audience     = "blood-supply-api"
)

// User — кто входит в провайдере.
type User struct {
	Username string
	Roles    []auth.Role
	ClinicID *int64
}

type grant struct {
	challenge   string
	nonce       string
	redirectURI string
	user        User
}

// Provider — фальшивый провайдер на httptest.Server.
type Provider struct {
	unavailable atomic.Bool
	URL         string // он же issuer

	key    *rsa.PrivateKey
	kid    string
	server *httptest.Server

	mu         sync.Mutex
	issuer     string
	user       User
	codes      map[string]grant
	refresh    map[string]User
	nonceForce *string
	accessTTL  time.Duration
	lastHint   string
	tokenCalls int
	// personal — класть в токены ФИО и email (как Keycloak со scope profile и email по умолчанию).
	personal bool
	// idSubject — подменённый sub в id_token (проверка «токены выданы разным пользователям»).
	idSubject string
}

// FullName и Email — персональные данные, которые выдаёт ненастроенный Keycloak.
const (
	FullName = "Иванова Мария Петровна"
	Email    = "m.ivanova@clinic.example"
)

// New поднимает провайдера; закрывается в t.Cleanup. По умолчанию входит оператор клиники 1, а токены
// содержат ФИО и email, как у Keycloak без настройки клиента; SetPersonalClaims(false) — как настроенный
// realm-blood-supply.json (клиент web без scope profile и email).
func New(t testing.TB) *Provider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	clinic := int64(1)
	p := &Provider{
		key: key, kid: "oidctest-1",
		user:      User{Username: "operator-c001", Roles: []auth.Role{auth.RoleOperator}, ClinicID: &clinic},
		codes:     map[string]grant{},
		refresh:   map[string]User{},
		accessTTL: 5 * time.Minute,
		personal:  true,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /jwks", p.jwks)
	mux.HandleFunc("GET /authorize", p.authorize)
	mux.HandleFunc("POST /token", p.token)
	mux.HandleFunc("GET /logout", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p.unavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	p.URL = p.server.URL
	t.Cleanup(p.server.Close)
	return p
}

// SetUnavailable имитирует отказ провайдера после уже состоявшегося входа.
func (p *Provider) SetUnavailable(on bool) { p.unavailable.Store(on) }

// SetPersonalClaims включает или выключает ФИО и email (name, given_name, family_name, email) в токенах.
func (p *Provider) SetPersonalClaims(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.personal = on
}

// ForceIDTokenSubject подменяет sub в следующих id_token.
func (p *Provider) ForceIDTokenSubject(sub string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.idSubject = sub
}

// UseIssuer задаёт iss в discovery и токенах, отличный от адреса сервера: так ведёт себя Keycloak в compose,
// где браузер видит localhost:8180, а web ходит по внутреннему адресу keycloak:8080.
func (p *Provider) UseIssuer(iss string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.issuer = iss
}

// Issuer — значение iss в токенах.
func (p *Provider) Issuer() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.issuer != "" {
		return p.issuer
	}
	return p.URL
}

// LoginAs задаёт, кто войдёт при следующем переходе на authorization endpoint.
func (p *Provider) LoginAs(u User) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.user = u
}

// ForceNonce подменяет nonce в следующих id_token (проверка отказа по nonce).
func (p *Provider) ForceNonce(n string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nonceForce = &n
}

// SetAccessTTL задаёт срок жизни access token.
func (p *Provider) SetAccessTTL(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accessTTL = d
}

// RevokeRefreshTokens делает все выданные refresh token недействительными (сессия IdP завершена).
func (p *Provider) RevokeRefreshTokens() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refresh = map[string]User{}
}

// LastLoginHint — login_hint последнего перехода на authorization endpoint.
func (p *Provider) LastLoginHint() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastHint
}

// TokenCalls — сколько раз вызывался эндпоинт токенов.
func (p *Provider) TokenCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tokenCalls
}

// Authorize проходит authorization endpoint как браузер и возвращает адрес callback с code и state.
func (p *Provider) Authorize(t testing.TB, authURL string) *url.URL {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, authURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorization endpoint ответил %d", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// AccessToken выпускает access token пользователю напрямую (для тестов вызовов сервисов).
func (p *Provider) AccessToken(t testing.TB, u User, ttl time.Duration) string {
	t.Helper()
	tok, err := p.sign(p.accessClaims(u, ttl))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (p *Provider) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                p.Issuer(),
		"authorization_endpoint":                p.URL + "/authorize",
		"token_endpoint":                        p.URL + "/token",
		"jwks_uri":                              p.URL + "/jwks",
		"end_session_endpoint":                  p.URL + "/logout",
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (p *Provider) jwks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: &p.key.PublicKey, KeyID: p.kid, Algorithm: string(jose.RS256), Use: "sig"},
	}})
}

func (p *Provider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != ClientID || q.Get("response_type") != "code" ||
		q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("redirect_uri") == "" {
		http.Error(w, "неверный запрос авторизации", http.StatusBadRequest)
		return
	}
	code := random()
	p.mu.Lock()
	p.lastHint = q.Get("login_hint")
	p.codes[code] = grant{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), redirectURI: q.Get("redirect_uri"), user: p.user}
	p.mu.Unlock()
	target, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "неверный redirect_uri", http.StatusBadRequest)
		return
	}
	tq := target.Query()
	tq.Set("code", code)
	tq.Set("state", q.Get("state"))
	target.RawQuery = tq.Encode()
	//nolint:gosec // фальшивый провайдер для тестов: redirect_uri задаёт тест, как его задаёт клиент Keycloak
	http.Redirect(w, r, target.String(), http.StatusFound)
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		tokenError(w, "invalid_request")
		return
	}
	p.mu.Lock()
	p.tokenCalls++
	p.mu.Unlock()
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id, err := url.QueryUnescape(id); err != nil || id != ClientID ||
		subtle.ConstantTimeCompare([]byte(secret), []byte(ClientSecret)) != 1 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		p.mu.Lock()
		g, found := p.codes[r.PostForm.Get("code")]
		delete(p.codes, r.PostForm.Get("code"))
		p.mu.Unlock()
		if !found || g.redirectURI != r.PostForm.Get("redirect_uri") {
			tokenError(w, "invalid_grant")
			return
		}
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
			tokenError(w, "invalid_grant")
			return
		}
		p.issue(w, g.user, g.nonce, true)
	case "refresh_token":
		p.mu.Lock()
		u, found := p.refresh[r.PostForm.Get("refresh_token")]
		delete(p.refresh, r.PostForm.Get("refresh_token"))
		p.mu.Unlock()
		if !found {
			tokenError(w, "invalid_grant")
			return
		}
		p.issue(w, u, "", false)
	default:
		tokenError(w, "unsupported_grant_type")
	}
}

func (p *Provider) issue(w http.ResponseWriter, u User, nonce string, withID bool) {
	p.mu.Lock()
	ttl := p.accessTTL
	if p.nonceForce != nil {
		nonce = *p.nonceForce
	}
	refresh := random()
	p.refresh[refresh] = u
	personal, idSubject := p.personal, p.idSubject
	p.mu.Unlock()

	access, err := p.sign(p.accessClaims(u, ttl))
	if err != nil {
		tokenError(w, "server_error")
		return
	}
	out := map[string]any{
		"access_token": access, "token_type": "Bearer", "expires_in": int(ttl.Seconds()), "refresh_token": refresh,
	}
	if withID {
		now := time.Now()
		claims := map[string]any{
			"iss": p.Issuer(), "sub": "sub-" + u.Username, "aud": ClientID, "azp": ClientID,
			"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(ttl).Unix(), "nonce": nonce,
			"preferred_username": u.Username,
		}
		if idSubject != "" {
			claims["sub"] = idSubject
		}
		if personal {
			addPersonal(claims)
		}
		id, err := p.sign(claims)
		if err != nil {
			tokenError(w, "server_error")
			return
		}
		out["id_token"] = id
	}
	writeJSON(w, http.StatusOK, out)
}

func (p *Provider) accessClaims(u User, ttl time.Duration) map[string]any {
	now := time.Now()
	roles := make([]string, 0, len(u.Roles))
	for _, r := range u.Roles {
		roles = append(roles, string(r))
	}
	c := map[string]any{
		"iss": p.Issuer(), "sub": "sub-" + u.Username, "aud": Audience, "azp": ClientID, "typ": "Bearer",
		"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(ttl).Unix(),
		"preferred_username": u.Username,
		"realm_access":       map[string]any{"roles": roles},
	}
	if u.ClinicID != nil {
		c["clinic_id"] = *u.ClinicID
	}
	p.mu.Lock()
	personal := p.personal
	p.mu.Unlock()
	if personal {
		addPersonal(c)
	}
	return c
}

// addPersonal — claims стандартных scope profile и email Keycloak.
func addPersonal(c map[string]any) {
	c["name"] = FullName
	c["given_name"] = "Мария"
	c["family_name"] = "Иванова"
	c["email"] = Email
	c["email_verified"] = true
}

func (p *Provider) sign(claims map[string]any) (string, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: p.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", p.kid))
	if err != nil {
		return "", err
	}
	return jwt.Signed(signer).Claims(claims).Serialize()
}

func tokenError(w http.ResponseWriter, code string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func random() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
