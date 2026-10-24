package inventoryclient

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/caarlos0/env/v11"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
)

// TokenConfig — служебный клиент Keycloak для вызова inventory (client credentials, роль SERVICE).
type TokenConfig struct {
	// TokenURL — эндпоинт токенов; пусто — из BS_OIDC_ISSUER.
	TokenURL     string `env:"BS_SERVICE_TOKEN_URL"`
	ClientID     string `env:"BS_SERVICE_CLIENT_ID" envDefault:"services"`
	ClientSecret string `env:"BS_SERVICE_CLIENT_SECRET"`
}

// LoadConfig читает настройки клиента и служебного токена из окружения.
func LoadConfig() (Config, TokenConfig, error) {
	var c Config
	var t TokenConfig
	if err := env.Parse(&c); err != nil {
		return Config{}, TokenConfig{}, fmt.Errorf("конфигурация клиента inventory: %w", err)
	}
	if err := env.Parse(&t); err != nil {
		return Config{}, TokenConfig{}, fmt.Errorf("конфигурация служебного токена: %w", err)
	}
	return c, t, nil
}

// NewAuthorizer — источник служебного токена. При отключённой проверке JWT (только локальная отладка,
// ADR-0015) — токен отладочного проверяющего inventory с ролью SERVICE.
func NewAuthorizer(t TokenConfig, issuer string, authDisabled bool) (Authorizer, error) {
	if authDisabled {
		return devToken{}, nil
	}
	if t.TokenURL == "" {
		if issuer == "" {
			return nil, errors.New("служебный токен: нужен BS_SERVICE_TOKEN_URL или BS_OIDC_ISSUER")
		}
		t.TokenURL = auth.TokenURL(issuer)
	}
	st, err := auth.NewServiceToken(auth.ClientCredentialsConfig{TokenURL: t.TokenURL, ClientID: t.ClientID, ClientSecret: t.ClientSecret})
	if err != nil {
		return nil, err
	}
	return st, nil
}

// devToken — токен в формате отладочного проверяющего libs/runtime: dev:<имя>:<роль>.
type devToken struct{}

func (devToken) Authorize(r *http.Request) error {
	r.Header.Set("Authorization", "Bearer dev:transfer:"+string(auth.RoleService))
	return nil
}
