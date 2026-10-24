// Package svcclient — вызовы сервисов из web от имени пользователя: JWT из сессии в Authorization,
// корреляция, таймаут и предохранитель libs/httpx. 401 от сервиса значит, что токен больше не принимается,
// и пользователь отправляется на повторный вход (LoginOnUnauthorized).
package svcclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
)

// ErrUnauthorized — нет токена пользователя или сервис ответил 401.
var ErrUnauthorized = errors.New("сервис не принял токен пользователя")

// StatusError — сервис ответил ошибкой, отличной от 401.
type StatusError struct {
	Service string
	Code    int
	Problem httpx.Problem
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s ответил %d: %s", e.Service, e.Code, e.Problem.Title)
}

type tokenKey struct{}

// WithToken кладёт access token пользователя в контекст запроса (делает проверка сессии).
func WithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tokenKey{}, token)
}

// Token — access token пользователя из контекста.
func Token(ctx context.Context) string {
	t, _ := ctx.Value(tokenKey{}).(string)
	return t
}

// Client — клиент одного сервиса.
type Client struct {
	name string
	base string
	http *httpx.Client
}

// New создаёт клиента сервиса name по адресу baseURL.
func New(name, baseURL string) *Client {
	return &Client{
		name: name, base: strings.TrimRight(baseURL, "/"),
		http: httpx.NewClient(httpx.ClientConfig{Name: name, Timeout: 5 * time.Second, Retries: 1}),
	}
}

// Do выполняет запрос с токеном пользователя. 401 — ErrUnauthorized (тело закрыто).
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	token := Token(ctx)
	if token == "" {
		return nil, ErrUnauthorized
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.name, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.name, err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		return nil, ErrUnauthorized
	}
	return resp, nil
}

// GetJSON — GET с разбором JSON при 200.
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	resp, err := c.Do(ctx, http.MethodGet, path, nil, "") //nolint:bodyclose // тело закрывает decode
	if err != nil {
		return err
	}
	return c.decode(resp, out)
}

// PostJSON — POST с телом JSON и разбором ответа при 2xx.
func (c *Client) PostJSON(ctx context.Context, path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("%s: %w", c.name, err)
	}
	resp, err := c.Do(ctx, http.MethodPost, path, bytes.NewReader(b), "application/json") //nolint:bodyclose // тело закрывает decode
	if err != nil {
		return err
	}
	return c.decode(resp, out)
}

func (c *Client) PutJSON(ctx context.Context, path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("%s: %w", c.name, err)
	}
	resp, err := c.Do(ctx, http.MethodPut, path, bytes.NewReader(b), "application/json") //nolint:bodyclose // тело закрывает decode
	if err != nil {
		return err
	}
	return c.decode(resp, out)
}

func (c *Client) decode(resp *http.Response, out any) error {
	defer func() { _ = resp.Body.Close() }()
	body := io.LimitReader(resp.Body, 8<<20)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		se := &StatusError{Service: c.name, Code: resp.StatusCode}
		_ = json.NewDecoder(body).Decode(&se.Problem)
		return se
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(body).Decode(out); err != nil {
		return fmt.Errorf("%s: ответ не разобран: %w", c.name, err)
	}
	return nil
}

// LoginOnUnauthorized отправляет пользователя на вход, если err — ErrUnauthorized, и возвращает true.
// Обычный запрос получает 303 на /login с адресом возврата (для GET), запрос HTMX — заголовок HX-Redirect.
func LoginOnUnauthorized(w http.ResponseWriter, r *http.Request, err error) bool {
	if !errors.Is(err, ErrUnauthorized) {
		return false
	}
	RedirectToLogin(w, r)
	return true
}

// RedirectToLogin отправляет пользователя на вход: 303 на /login (для GET — с адресом возврата),
// для HTMX — 401 с HX-Redirect, чтобы страница перешла целиком, а не вставила форму входа во фрагмент.
func RedirectToLogin(w http.ResponseWriter, r *http.Request) {
	target := "/login"
	if r.Method == http.MethodGet {
		target += "?next=" + url.QueryEscape(r.URL.RequestURI())
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
