package svcclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/correlation"
)

func service(t *testing.T, status int, body string) (*httptest.Server, *http.Header) {
	t.Helper()
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

// JWT пользователя из сессии уходит в сервис вместе с корреляцией.
func TestPassesUserToken(t *testing.T) {
	srv, got := service(t, http.StatusOK, `{"items":[1,2]}`)
	c := New("transfer", srv.URL)
	ctx := WithToken(context.Background(), "user-jwt")
	ctx = correlation.With(ctx, correlation.IDs{Correlation: "corr-7"})
	var out struct {
		Items []int `json:"items"`
	}
	if err := c.GetJSON(ctx, "/obligations", &out); err != nil {
		t.Fatal(err)
	}
	if got.Get("Authorization") != "Bearer user-jwt" || got.Get("X-Correlation-Id") != "corr-7" {
		t.Fatalf("заголовки: %v", *got)
	}
	if len(out.Items) != 2 {
		t.Fatalf("ответ разобран неверно: %+v", out)
	}
}

// 401 от сервиса — токен пользователя больше не принимается, нужен повторный вход.
func TestUnauthorizedFromService(t *testing.T) {
	srv, _ := service(t, http.StatusUnauthorized, `{"title":"Требуется вход","status":401}`)
	c := New("transfer", srv.URL)
	err := c.GetJSON(WithToken(context.Background(), "expired"), "/obligations", &struct{}{})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("ожидалась ErrUnauthorized, получено %v", err)
	}
}

func TestNoTokenIsUnauthorizedWithoutRequest(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	t.Cleanup(srv.Close)
	err := New("transfer", srv.URL).GetJSON(context.Background(), "/x", &struct{}{})
	if !errors.Is(err, ErrUnauthorized) || called {
		t.Fatalf("без токена — ErrUnauthorized без запроса: %v, запрос %v", err, called)
	}
}

func TestOtherStatusIsStatusError(t *testing.T) {
	srv, _ := service(t, http.StatusForbidden, `{"title":"Недостаточно прав","status":403,"detail":"Операция недоступна вашей роли."}`)
	err := New("transfer", srv.URL).GetJSON(WithToken(context.Background(), "t"), "/x", &struct{}{})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusForbidden || se.Problem.Title != "Недостаточно прав" {
		t.Fatalf("ожидалась StatusError 403 с problem+json, получено %v", err)
	}
}

func TestPostJSON(t *testing.T) {
	srv, got := service(t, http.StatusCreated, `{"id":"o-1"}`)
	var out struct {
		ID string `json:"id"`
	}
	if err := New("transfer", srv.URL).PostJSON(WithToken(context.Background(), "t"), "/x", map[string]int{"n": 1}, &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != "o-1" || got.Get("Content-Type") != "application/json" {
		t.Fatalf("POST: %+v %v", out, *got)
	}
}

// 401 от сервиса превращается в переход на вход: обычный запрос — 303 с адресом возврата, HTMX — HX-Redirect.
func TestLoginOnUnauthorized(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/tasks?status=PENDING", nil)
	if !LoginOnUnauthorized(rec, r, ErrUnauthorized) {
		t.Fatal("401 должен обрабатываться")
	}
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?next=%2Ftasks%3Fstatus%3DPENDING" {
		t.Fatalf("переход на вход: %d %s", rec.Code, rec.Header().Get("Location"))
	}

	rec = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/tasks/1/confirm", nil)
	r.Header.Set("HX-Request", "true")
	if !LoginOnUnauthorized(rec, r, ErrUnauthorized) {
		t.Fatal("401 должен обрабатываться")
	}
	if rec.Header().Get("HX-Redirect") != "/login" {
		t.Fatalf("HTMX: %v", rec.Header())
	}

	if LoginOnUnauthorized(httptest.NewRecorder(), r, &StatusError{Code: 500}) {
		t.Fatal("другие ошибки не превращаются во вход")
	}
}
