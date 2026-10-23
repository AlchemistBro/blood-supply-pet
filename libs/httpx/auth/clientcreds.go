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
	// RefreshBefore — за сколько до истечения начинать обновление (по умолчанию 30 с,
	// но не больше половины срока жизни токена).
	RefreshBefore time.Duration
	// RefreshTimeout — предел одного запроса токена (по умолчанию 2 с). Запрос не зависит от контекста
	// вызывающего: его могут ждать несколько вызовов сразу.
	RefreshTimeout time.Duration
	// HTTPClient — клиент для запроса токена; по умолчанию с таймаутом 5 с.
	HTTPClient *http.Client
	// Clock — часы для срока жизни кэша и паузы после сбоя; по умолчанию системные.
	Clock clock.Clock
}

// Пауза перед следующей попыткой после сбоя: удваивается до предела, сбрасывается после успеха.
const (
	refreshBackoffMin = time.Second
	refreshBackoffMax = 30 * time.Second
)

// ServiceToken — источник служебного токена с кэшем.
//
// Вызов transfer → inventory укладывается в 3 с (ADR-0004), поэтому получение токена не должно его съедать:
//   - пока токен не истёк, он отдаётся сразу, даже в окне обновления; обновление идёт одним фоновым запросом;
//   - сетевой запрос выполняется вне мьютекса, одновременные вызовы делят один запрос (singleflight);
//   - после сбоя следующая попытка не раньше паузы, чтобы не долбить недоступный Keycloak;
//   - вызывающий без действующего токена ждёт запрос не дольше своего ctx.
type ServiceToken struct {
	cfg ClientCredentialsConfig

	mu          sync.Mutex
	token       string
	expiresAt   time.Time
	refreshAt   time.Time
	inflight    *refreshCall
	failures    int
	nextAttempt time.Time
	lastErr     error
}

type refreshCall struct {
	done chan struct{}
	err  error
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
	if cfg.RefreshTimeout <= 0 {
		cfg.RefreshTimeout = 2 * time.Second
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
	now := s.cfg.Clock.Now()
	valid := s.token != "" && now.Before(s.expiresAt)
	if valid && now.Before(s.refreshAt) {
		token := s.token
		s.mu.Unlock()
		return token, nil
	}
	call := s.startRefreshLocked(now)
	if valid {
		// Окно обновления: действующий токен отдаём сразу, обновление идёт в фоне.
		token := s.token
		s.mu.Unlock()
		return token, nil
	}
	if call == nil {
		err := s.lastErr
		s.mu.Unlock()
		return "", fmt.Errorf("служебный токен: пауза после сбоя: %w", err)
	}
	s.mu.Unlock()

	select {
	case <-call.done:
	case <-ctx.Done():
		return "", fmt.Errorf("служебный токен: %w", ctx.Err())
	}
	if call.err != nil {
		return "", call.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == "" {
		return "", errors.New("служебный токен: не получен")
	}
	return s.token, nil
}

// startRefreshLocked запускает фоновый запрос токена или возвращает уже идущий. Во время паузы после
// сбоя возвращает nil. Вызывается под s.mu.
func (s *ServiceToken) startRefreshLocked(now time.Time) *refreshCall {
	if s.inflight != nil {
		return s.inflight
	}
	if now.Before(s.nextAttempt) {
		return nil
	}
	call := &refreshCall{done: make(chan struct{})}
	s.inflight = call
	go s.refresh(call, now)
	return call
}

func (s *ServiceToken) refresh(call *refreshCall, started time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.RefreshTimeout)
	defer cancel()
	token, ttl, err := s.fetch(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.failures++
		backoff := refreshBackoffMin << min(s.failures-1, 5)
		s.nextAttempt = started.Add(min(backoff, refreshBackoffMax))
		s.lastErr = err
	} else {
		// Срок считаем от начала запроса: так кэш не переживёт настоящий токен.
		early := min(s.cfg.RefreshBefore, ttl/2)
		s.token, s.expiresAt, s.refreshAt = token, started.Add(ttl), started.Add(ttl-early)
		s.failures, s.nextAttempt, s.lastErr = 0, time.Time{}, nil
	}
	call.err = err
	s.inflight = nil
	close(call.done)
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
