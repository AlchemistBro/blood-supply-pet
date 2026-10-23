// Package authtest — тестовый поставщик идентификации: генерирует ключ RSA, отдаёт JWKS по HTTP,
// выпускает токены с нужными ролями и клиникой и обслуживает выдачу служебных токенов по client credentials
// (как Keycloak: /realms/<realm>/protocol/openid-connect/token). Интеграционные тесты сервисов не нуждаются в Keycloak.
package authtest

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
)

// ServiceAudience — аудитория служебного токена: в Keycloak её добавляет маппер клиента services.
const ServiceAudience = "blood-supply-api"

// IdP — тестовый поставщик.
type IdP struct {
	Issuer string
	key    *rsa.PrivateKey
	kid    string
	server *httptest.Server

	mu      sync.Mutex
	clients map[string]*serviceClient
	failing bool
}

type serviceClient struct {
	secret string
	roles  []auth.Role
	ttl    time.Duration
	issued int
}

// New поднимает JWKS-сервер и эндпоинт токенов; закрывается в t.Cleanup.
func New(t testing.TB) *IdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &IdP{key: key, kid: "test-key-1", clients: map[string]*serviceClient{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: idp.kid, Algorithm: string(jose.RS256), Use: "sig"}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(set)
	})
	mux.HandleFunc("POST /realms/test/protocol/openid-connect/token", idp.serveToken)
	idp.server = httptest.NewServer(mux)
	idp.Issuer = idp.server.URL + "/realms/test"
	t.Cleanup(idp.server.Close)
	return idp
}

// JWKSURL — адрес ключей.
func (i *IdP) JWKSURL() string { return i.server.URL + "/jwks" }

// Verifier создаёт проверяющего, доверяющего этому IdP.
func (i *IdP) Verifier(t testing.TB, audience string) auth.Verifier {
	t.Helper()
	v, err := auth.NewVerifier(context.Background(), auth.Config{Issuer: i.Issuer, Audience: audience, JWKSURL: i.JWKSURL()})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// RegisterClient заводит конфиденциального клиента с сервисной учётной записью (как services в realm).
func (i *IdP) RegisterClient(clientID, secret string, roles []auth.Role, ttl time.Duration) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.clients[clientID] = &serviceClient{secret: secret, roles: append([]auth.Role(nil), roles...), ttl: ttl}
}

// IssuedTokens — сколько служебных токенов выдано клиенту (проверка кэша).
func (i *IdP) IssuedTokens(clientID string) int {
	i.mu.Lock()
	defer i.mu.Unlock()
	if c, ok := i.clients[clientID]; ok {
		return c.issued
	}
	return 0
}

// FailTokenRequests включает имитацию недоступности эндпоинта токенов (503).
func (i *IdP) FailTokenRequests(fail bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.failing = fail
}

func (i *IdP) serveToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if r.PostForm.Get("grant_type") != "client_credentials" {
		tokenError(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	i.mu.Lock()
	if i.failing {
		i.mu.Unlock()
		tokenError(w, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	c, known := i.clients[id]
	if !known || c.secret != secret {
		i.mu.Unlock()
		tokenError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	c.issued++
	roles, ttl := c.roles, c.ttl
	i.mu.Unlock()

	raw, err := i.sign(TokenOptions{Username: "service-account-" + id, Roles: roles, Audience: ServiceAudience, TTL: ttl})
	if err != nil {
		tokenError(w, http.StatusInternalServerError, "server_error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": raw, "token_type": "Bearer", "expires_in": int(ttl.Seconds()),
	})
}

func tokenError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

// TokenOptions — что положить в токен.
type TokenOptions struct {
	Subject  string
	Username string
	Roles    []auth.Role
	ClinicID *int64
	Audience string
	// TTL — срок жизни; отрицательный даёт просроченный токен.
	TTL time.Duration
}

// Token выпускает подписанный JWT.
func (i *IdP) Token(t testing.TB, o TokenOptions) string {
	t.Helper()
	raw, err := i.sign(o)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (i *IdP) sign(o TokenOptions) (string, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: i.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", i.kid))
	if err != nil {
		return "", fmt.Errorf("подписант: %w", err)
	}
	if o.TTL == 0 {
		o.TTL = time.Hour
	}
	if o.Subject == "" {
		o.Subject = o.Username
	}
	now := time.Now()
	std := jwt.Claims{
		Issuer:   i.Issuer,
		Subject:  o.Subject,
		IssuedAt: jwt.NewNumericDate(now.Add(-time.Minute)),
		Expiry:   jwt.NewNumericDate(now.Add(o.TTL)),
	}
	if o.Audience != "" {
		std.Audience = jwt.Audience{o.Audience}
	}
	roles := make([]string, 0, len(o.Roles))
	for _, r := range o.Roles {
		roles = append(roles, string(r))
	}
	private := map[string]any{
		"preferred_username": o.Username,
		"realm_access":       map[string]any{"roles": roles},
	}
	if o.ClinicID != nil {
		private["clinic_id"] = *o.ClinicID
	}
	raw, err := jwt.Signed(signer).Claims(std).Claims(private).Serialize()
	if err != nil {
		return "", fmt.Errorf("подпись токена: %w", err)
	}
	return raw, nil
}

// Clinic — помощник для указателя на клинику.
func Clinic(id int64) *int64 { return &id }
