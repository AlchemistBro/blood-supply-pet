// Package oidc — вход в web через Keycloak (ADR-0006, ADR-0018): authorization code + PKCE S256,
// проверка state (в server) и nonce (здесь), обновление по refresh token, выход через end-session.
// Роли и клиника берутся из access token, проверенного той же библиотекой libs/httpx/auth, что и в сервисах:
// web и сервисы видят одного и того же участника.
package oidc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/session"
)

// Ошибки входа.
var (
	// ErrNonce — nonce в id_token не совпал с выданным при переходе в Keycloak.
	ErrNonce = errors.New("nonce в id_token не совпал")
	// ErrToken — провайдер не выдал токены или они не прошли проверку.
	ErrToken = errors.New("токены провайдера не прошли проверку")
	// ErrPersonalData — в access token есть ФИО или контакты: такой токен нельзя хранить в cookie сессии
	// и пересылать сервисам (в системе нет ФИО). Исправляется настройкой клиента web в Keycloak.
	ErrPersonalData = errors.New("в access token есть персональные данные")
)

// personalClaims — claims стандартных scope profile, email, phone, address: в токенах системы их быть не должно.
var personalClaims = []string{
	"name", "given_name", "family_name", "middle_name", "nickname", "email", "phone_number", "address",
	"birthdate", "gender", "picture", "snils",
}

// Config — клиент web в realm blood-supply.
type Config struct {
	// Issuer — ожидаемый iss (публичный адрес realm, который видит браузер).
	Issuer string
	// DiscoveryURL — откуда читать discovery, если web видит Keycloak по другому адресу (compose:
	// http://keycloak:8080/realms/blood-supply). Пусто — Issuer.
	DiscoveryURL string
	ClientID     string
	ClientSecret string
	// RedirectURL — адрес callback web: http://localhost:8080/auth/callback.
	RedirectURL string
	// PostLogoutRedirectURL — куда Keycloak вернёт после выхода.
	PostLogoutRedirectURL string
	// Audience — ожидаемый aud access token (blood-supply-api); пусто — не проверять.
	Audience string
	// Timeout — таймаут запросов к Keycloak (по умолчанию 5 с).
	Timeout time.Duration
}

// Result — итог входа или обновления: участник и токены для сессии.
type Result struct {
	Principal    auth.Principal
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
}

// Client — OIDC-клиент web.
type Client struct {
	cfg        Config
	oauth      oauth2.Config
	idVerifier *gooidc.IDTokenVerifier
	access     auth.Verifier
	endSession string
	origin     string
	http       *http.Client
}

// New читает discovery и готовит проверку id_token и access token.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.RedirectURL == "" {
		return nil, errors.New("OIDC: нужны issuer, client_id и redirect_url")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	httpClient := &http.Client{Timeout: cfg.Timeout}
	pctx := gooidc.ClientContext(ctx, httpClient)
	discovery := cfg.Issuer
	if cfg.DiscoveryURL != "" && cfg.DiscoveryURL != cfg.Issuer {
		// iss в токенах — публичный адрес, документ discovery читается по внутреннему.
		discovery = cfg.DiscoveryURL
		pctx = gooidc.InsecureIssuerURLContext(pctx, cfg.Issuer)
	}
	provider, err := gooidc.NewProvider(pctx, discovery)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery %s: %w", discovery, err)
	}
	var meta struct {
		EndSession string `json:"end_session_endpoint"`
		JWKS       string `json:"jwks_uri"`
	}
	if err := provider.Claims(&meta); err != nil {
		return nil, fmt.Errorf("OIDC discovery: %w", err)
	}
	if meta.JWKS == "" {
		return nil, errors.New("OIDC discovery: нет jwks_uri")
	}
	meta.JWKS = backchannelURL(meta.JWKS, cfg)
	access, err := auth.NewVerifier(pctx, auth.Config{Issuer: cfg.Issuer, Audience: cfg.Audience, JWKSURL: meta.JWKS})
	if err != nil {
		return nil, fmt.Errorf("OIDC: проверка access token: %w", err)
	}
	endpoint := provider.Endpoint()
	endpoint.TokenURL = backchannelURL(endpoint.TokenURL, cfg)
	origin, err := originOf(endpoint.AuthURL)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery: authorization_endpoint: %w", err)
	}
	return &Client{
		cfg: cfg,
		oauth: oauth2.Config{
			ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURL: cfg.RedirectURL,
			Endpoint: endpoint,
			// Только openid: preferred_username, роли и clinic_id кладут мапперы клиента web, а scope profile и
			// email не запрашиваются — с ними Keycloak положил бы в токен ФИО и email (ADR-0018).
			Scopes: []string{gooidc.ScopeOpenID},
		},
		idVerifier: gooidc.NewVerifier(cfg.Issuer, gooidc.NewRemoteKeySet(pctx, meta.JWKS), &gooidc.Config{ClientID: cfg.ClientID}),
		access:     access,
		endSession: meta.EndSession,
		origin:     origin,
		http:       httpClient,
	}, nil
}

// backchannelURL сохраняет публичный issuer, но токены и ключи запрашивает по внутреннему адресу realm.
// Меняются только URL этого же realm: authorization и end-session остаются адресами для браузера.
func backchannelURL(raw string, cfg Config) string {
	if cfg.DiscoveryURL == "" || cfg.DiscoveryURL == cfg.Issuer {
		return raw
	}
	endpoint, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	issuer, err := url.Parse(cfg.Issuer)
	if err != nil {
		return raw
	}
	internal, err := url.Parse(cfg.DiscoveryURL)
	if err != nil {
		return raw
	}
	prefix := strings.TrimRight(issuer.Path, "/")
	if endpoint.Scheme != issuer.Scheme || endpoint.Host != issuer.Host || !strings.HasPrefix(endpoint.Path, prefix+"/") {
		return raw
	}
	endpoint.Scheme, endpoint.Host = internal.Scheme, internal.Host
	endpoint.Path = strings.TrimRight(internal.Path, "/") + strings.TrimPrefix(endpoint.Path, prefix)
	endpoint.RawPath = ""
	return endpoint.String()
}

// NewLoginState — случайные state, nonce и PKCE verifier для одного входа; next — локальный путь возврата.
func NewLoginState(next string) (session.LoginState, error) {
	st, err := session.RandomToken()
	if err != nil {
		return session.LoginState{}, err
	}
	nonce, err := session.RandomToken()
	if err != nil {
		return session.LoginState{}, err
	}
	return session.LoginState{State: st, Nonce: nonce, Verifier: oauth2.GenerateVerifier(), Next: next}, nil
}

// AuthURL — адрес перехода в Keycloak. loginHint подставляет логин (блок «Демо-доступ»).
func (c *Client) AuthURL(st session.LoginState, loginHint string) string {
	opts := []oauth2.AuthCodeOption{gooidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier)}
	if loginHint != "" {
		opts = append(opts, oauth2.SetAuthURLParam("login_hint", loginHint))
	}
	return c.oauth.AuthCodeURL(st.State, opts...)
}

// Exchange обменивает код на токены, проверяет id_token (подпись, iss, aud, срок, nonce) и access token.
// state проверяет вызывающий до обмена.
func (c *Client) Exchange(ctx context.Context, code string, st session.LoginState) (Result, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, c.http)
	tok, err := c.oauth.Exchange(ctx, code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return Result{}, fmt.Errorf("%w: обмен кода: %w", ErrToken, err)
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok || rawID == "" {
		return Result{}, fmt.Errorf("%w: нет id_token", ErrToken)
	}
	idTok, err := c.idVerifier.Verify(ctx, rawID)
	if err != nil {
		return Result{}, fmt.Errorf("%w: id_token: %w", ErrToken, err)
	}
	if idTok.Nonce == "" || !session.Equal(idTok.Nonce, st.Nonce) {
		return Result{}, ErrNonce
	}
	res, err := c.result(ctx, tok)
	if err != nil {
		return Result{}, err
	}
	if res.Principal.Subject != idTok.Subject {
		return Result{}, fmt.Errorf("%w: id_token и access token выданы разным пользователям", ErrToken)
	}
	return res, nil
}

// Refresh получает новый access token по refresh token (Keycloak при этом выдаёт и новый refresh token).
func (c *Client) Refresh(ctx context.Context, refreshToken string) (Result, error) {
	if refreshToken == "" {
		return Result{}, fmt.Errorf("%w: нет refresh token", ErrToken)
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, c.http)
	tok, err := c.oauth.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken, Expiry: time.Unix(1, 0)}).Token()
	if err != nil {
		return Result{}, fmt.Errorf("%w: обновление: %w", ErrToken, err)
	}
	return c.result(ctx, tok)
}

func (c *Client) result(ctx context.Context, tok *oauth2.Token) (Result, error) {
	p, err := c.access.Verify(ctx, tok.AccessToken)
	if err != nil {
		return Result{}, fmt.Errorf("%w: access token: %w", ErrToken, err)
	}
	// Токен хранится в cookie целиком (его пересылают сервисам, подпись не позволяет вырезать поля),
	// поэтому токен с ФИО или контактами не принимается вовсе.
	if found, err := personalClaimsIn(tok.AccessToken); err != nil {
		return Result{}, fmt.Errorf("%w: access token: %w", ErrToken, err)
	} else if len(found) > 0 {
		return Result{}, fmt.Errorf("%w: %s", ErrPersonalData, strings.Join(found, ", "))
	}
	return Result{Principal: p, AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, Expiry: tok.Expiry}, nil
}

// LogoutURL — end-session Keycloak. id_token_hint не передаётся (id_token не хранится, ADR-0018),
// поэтому Keycloak может показать страницу подтверждения выхода.
func (c *Client) LogoutURL() string {
	if c.endSession == "" {
		return c.cfg.PostLogoutRedirectURL
	}
	q := url.Values{"client_id": {c.cfg.ClientID}}
	if c.cfg.PostLogoutRedirectURL != "" {
		q.Set("post_logout_redirect_uri", c.cfg.PostLogoutRedirectURL)
	}
	sep := "?"
	if strings.Contains(c.endSession, "?") {
		sep = "&"
	}
	return c.endSession + sep + q.Encode()
}

// ProviderOrigin — источник страниц Keycloak, который видит браузер (для CSP form-action).
func (c *Client) ProviderOrigin() string { return c.origin }

// personalClaimsIn — имена claims с персональными данными в JWT (подпись уже проверена).
func personalClaimsIn(raw string) ([]string, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("не JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("тело JWT: %w", err)
	}
	var claims map[string]json.RawMessage
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("тело JWT: %w", err)
	}
	var found []string
	for _, k := range personalClaims {
		if _, ok := claims[k]; ok {
			found = append(found, k)
		}
	}
	return found, nil
}

func originOf(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("адрес %q: %w", raw, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("адрес %q без схемы или хоста", raw)
	}
	return u.Scheme + "://" + u.Host, nil
}
