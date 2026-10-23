package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
)

// ClientCredentialsConfig — служебный клиент Keycloak (OAuth 2.0 client credentials). В стенде это клиент
// services из realm blood-supply: его сервисная учётная запись имеет роль SERVICE (ADR-0004, ADR-0006).
type ClientCredentialsConfig struct {
	// TokenURL — эндпоинт токенов; для Keycloak — TokenURL(issuer).
	TokenURL     string
	ClientID     string
	ClientSecret string
	// RefreshBefore — за сколько до истечения получать новый токен (по умолчанию 30 с,
	// но не больше половины срока жизни токена).
	RefreshBefore time.Duration
	// HTTPClient — клиент для запроса токена; по умолчанию с таймаутом 5 с.
	HTTPClient *http.Client
	// Clock — часы для срока жизни кэша; по умолчанию системные.
	Clock clock.Clock
}

// ServiceToken — источник служебного токена с кэшем. Токен берётся из кэша до окна обновления,
// в окне обновляется заранее. Если Keycloak в этот момент недоступен, отдаётся прежний токен, пока он
// не истёк: кратковременный сбой IdP не должен ронять внутренний вызов transfer → inventory.
// Безопасен для одновременного использования; одновременные вызовы делают один запрос.
type ServiceToken struct {
	cfg       ClientCredentialsConfig
	mu        sync.Mutex
	token     string
	expiresAt time.Time
	refreshAt time.Time
}

// TokenURL строит адрес эндпоинта токенов Keycloak по адресу realm (issuer).
func TokenURL(issuer string) string {
	return strings.TrimRight(issuer, "/") + "/protocol/openid-connect/token"
}

// NewServiceToken проверяет конфигурацию и создаёт источник. Сетевых запросов не делает.
func NewServiceToken(cfg ClientCredentialsConfig) (*ServiceToken, error) {
	if cfg.TokenURL == "" || cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("служебный токен: нужны адрес токенов, клиент и секрет")
	}
	if cfg.RefreshBefore <= 0 {
		cfg.RefreshBefore = 30 * time.Second
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.System{}
	}
	return &ServiceToken{cfg: cfg}, nil
}

// Token возвращает действующий служебный токен.
func (s *ServiceToken) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cfg.Clock.Now()
	if s.token != "" && now.Before(s.refreshAt) {
		return s.token, nil
	}
	token, ttl, err := s.fetch(ctx)
	if err != nil {
		if s.token != "" && now.Before(s.expiresAt) {
			return s.token, nil
		}
		return "", err
	}
	early := min(s.cfg.RefreshBefore, ttl/2)
	s.token, s.expiresAt, s.refreshAt = token, now.Add(ttl), now.Add(ttl-early)
	return token, nil
}

// Authorize ставит служебный токен в заголовок Authorization запроса.
func (s *ServiceToken) Authorize(req *http.Request) error {
	token, err := s.Token(req.Context())
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
	Error       string `json:"error"`
}

func (s *ServiceToken) fetch(ctx context.Context) (string, time.Duration, error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("служебный токен: запрос: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(s.cfg.ClientID), url.QueryEscape(s.cfg.ClientSecret))
	//nolint:gosec // адрес эндпоинта токенов задаёт конфигурация сервиса, пользовательского ввода здесь нет
	resp, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("служебный токен: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var tr tokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr); err != nil {
		return "", 0, fmt.Errorf("служебный токен: ответ %d не разобран: %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("служебный токен: ответ %d %s", resp.StatusCode, tr.Error)
	}
	if tr.AccessToken == "" || tr.ExpiresIn <= 0 {
		return "", 0, errors.New("служебный токен: в ответе нет токена или срока жизни")
	}
	return tr.AccessToken, time.Duration(tr.ExpiresIn) * time.Second, nil
}
