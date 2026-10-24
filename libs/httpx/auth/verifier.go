package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Principal — кто сделал запрос: из проверенного JWT.
type Principal struct {
	Subject  string
	Username string
	Roles    []Role
	// ClinicID — клиника, от имени которой работает пользователь; nil для аналитика, дежурного, ИТ-службы.
	ClinicID *int64
	Email    string
}

// HasRole проверяет наличие роли.
func (p Principal) HasRole(r Role) bool {
	for _, x := range p.Roles {
		if x == r {
			return true
		}
	}
	return false
}

// PrimaryRole — первая роль из списка (для аудита).
func (p Principal) PrimaryRole() string {
	if len(p.Roles) == 0 {
		return ""
	}
	return string(p.Roles[0])
}

// Verifier проверяет сырой токен и возвращает участника.
type Verifier interface {
	Verify(ctx context.Context, rawToken string) (Principal, error)
}

// ErrUnauthorized — токен отсутствует, просрочен или не прошёл проверку.
var ErrUnauthorized = errors.New("не авторизован")

// Config проверки токенов.
type Config struct {
	// Issuer — адрес realm Keycloak: http://keycloak:8180/realms/blood-supply.
	Issuer string
	// Audience — ожидаемый aud; пусто — не проверять (демо).
	Audience string
	// JWKSURL — явный адрес ключей; если пусто, берётся из OIDC discovery.
	JWKSURL string
	// ClockSkew — допустимый разбег часов.
	ClockSkew time.Duration
}

type oidcVerifier struct {
	v *oidc.IDTokenVerifier
}

// NewVerifier создаёт проверяющего. С JWKSURL не ходит в discovery: удобно для тестов и
// для сервисов, у которых нет доступа к Keycloak по внешнему адресу.
func NewVerifier(ctx context.Context, cfg Config) (Verifier, error) {
	oc := &oidc.Config{
		SkipClientIDCheck:    cfg.Audience == "",
		ClientID:             cfg.Audience,
		SupportedSigningAlgs: []string{oidc.RS256, oidc.ES256},
	}
	if cfg.ClockSkew > 0 {
		oc.Now = func() time.Time { return time.Now().Add(-cfg.ClockSkew) }
	}
	if cfg.JWKSURL != "" {
		ks := oidc.NewRemoteKeySet(ctx, cfg.JWKSURL)
		return &oidcVerifier{v: oidc.NewVerifier(cfg.Issuer, ks, oc)}, nil
	}
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery %s: %w", cfg.Issuer, err)
	}
	return &oidcVerifier{v: provider.Verifier(oc)}, nil
}

type claims struct {
	PreferredUsername string          `json:"preferred_username"`
	Email             string          `json:"email"`
	ClinicID          json.RawMessage `json:"clinic_id"`
	RealmAccess       struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
	// Roles — плоский список (служебные клиенты, тестовый IdP).
	Roles []string `json:"roles"`
}

func (o *oidcVerifier) Verify(ctx context.Context, raw string) (Principal, error) {
	tok, err := o.v.Verify(ctx, raw)
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %w", ErrUnauthorized, err)
	}
	var c claims
	if err := tok.Claims(&c); err != nil {
		return Principal{}, fmt.Errorf("%w: claims: %w", ErrUnauthorized, err)
	}
	p := Principal{Subject: tok.Subject, Username: c.PreferredUsername, Email: c.Email}
	for _, r := range append(c.RealmAccess.Roles, c.Roles...) {
		if role, ok := knownRole(r); ok {
			p.Roles = append(p.Roles, role)
		}
	}
	if id, ok := parseClinicID(c.ClinicID); ok {
		p.ClinicID = &id
	}
	return p, nil
}

func knownRole(s string) (Role, bool) {
	switch Role(s) {
	case RolePhysician, RoleOperator, RoleAnalyst, RoleDuty, RoleITService, RoleService:
		return Role(s), true
	}
	return "", false
}

// parseClinicID принимает число или строку: Keycloak отдаёт атрибуты пользователя строками.
func parseClinicID(v any) (int64, bool) {
	switch x := v.(type) {
	case json.RawMessage:
		// Числовой claim нельзя округлять через float64: это может сменить клинику.
		dec := json.NewDecoder(bytes.NewReader(x))
		dec.UseNumber()
		var value any
		if err := dec.Decode(&value); err != nil {
			return 0, false
		}
		return parseClinicID(value)
	case json.Number:
		n, err := strconv.ParseInt(string(x), 10, 64)
		return n, err == nil && n > 0
	case float64:
		if x <= 0 || x >= math.Exp2(63) || math.IsNaN(x) || math.Trunc(x) != x {
			return 0, false
		}
		return int64(x), true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n, err == nil && n > 0
	case []any:
		if len(x) == 1 {
			return parseClinicID(x[0])
		}
	}
	return 0, false
}

// BearerToken извлекает токен из заголовка Authorization.
func BearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}
