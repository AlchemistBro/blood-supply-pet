// Package authtest — тестовый поставщик идентификации: генерирует ключ RSA, отдаёт JWKS по HTTP
// и выпускает токены с нужными ролями и клиникой. Интеграционные тесты сервисов не нуждаются в Keycloak.
package authtest

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
)

// IdP — тестовый поставщик.
type IdP struct {
	Issuer string
	key    *rsa.PrivateKey
	kid    string
	server *httptest.Server
}

// New поднимает JWKS-сервер; закрывается в t.Cleanup.
func New(t testing.TB) *IdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &IdP{key: key, kid: "test-key-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: idp.kid, Algorithm: string(jose.RS256), Use: "sig"}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(set)
	})
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
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: i.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", i.kid))
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	return raw
}

// Clinic — помощник для указателя на клинику.
func Clinic(id int64) *int64 { return &id }
