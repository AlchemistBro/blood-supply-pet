package sender

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type staticToken string

func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

type captured struct {
	mu     sync.Mutex
	calls  int
	method string
	path   string
	auth   string
	ctype  string
	body   string
}

// snapshot — копия под мьютексом: обработчик пишет в другой горутине.
func (c *captured) snapshot() captured {
	c.mu.Lock()
	defer c.mu.Unlock()
	return captured{calls: c.calls, method: c.method, path: c.path, auth: c.auth, ctype: c.ctype, body: c.body}
}

func receiver(t *testing.T, status int, reply string) (*httptest.Server, *captured) {
	t.Helper()
	c := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.calls++
		c.method, c.path, c.auth, c.ctype, c.body = r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), string(b)
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

func TestSendPostsWithServiceToken(t *testing.T) {
	srv, rec := receiver(t, http.StatusAccepted, `{"emergency_forecast_id":"x"}`)
	s := New(srv.URL+"/", staticToken("svc-token"), 2*time.Second)
	res, err := s.Send(context.Background(), []byte(`{"scenario_version":"v1"}`))
	if err != nil {
		t.Fatal(err)
	}
	c := rec.snapshot()
	if res.Status != http.StatusAccepted || res.Body != `{"emergency_forecast_id":"x"}` {
		t.Fatalf("ответ %+v", res)
	}
	if c.method != http.MethodPost || c.path != ForecastPath || c.auth != "Bearer svc-token" || c.ctype != "application/json" {
		t.Fatalf("запрос %s %s, Authorization %q, Content-Type %q", c.method, c.path, c.auth, c.ctype)
	}
	if c.body != `{"scenario_version":"v1"}` {
		t.Fatalf("тело %s", c.body)
	}
}

func TestSendWithoutAuthHasNoHeader(t *testing.T) {
	srv, c := receiver(t, http.StatusOK, `{}`)
	if _, err := New(srv.URL, NoAuth{}, 2*time.Second).Send(context.Background(), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if auth := c.snapshot().auth; auth != "" {
		t.Fatalf("без токена ушёл заголовок Authorization %q", auth)
	}
}

func TestSendRefusesWithoutConfiguredToken(t *testing.T) {
	srv, c := receiver(t, http.StatusOK, `{}`)
	_, err := New(srv.URL, Unconfigured{}, 2*time.Second).Send(context.Background(), []byte(`{}`))
	if !errors.Is(err, ErrNoServiceToken) {
		t.Fatalf("ожидали ErrNoServiceToken, получили %v", err)
	}
	if c.snapshot().calls != 0 {
		t.Fatal("без токена запрос всё равно ушёл в planning")
	}
}

func TestSendReturnsRejectionAsResult(t *testing.T) {
	srv, _ := receiver(t, http.StatusConflict, `{"title":"версия сценария уже принята"}`)
	res, err := New(srv.URL, NoAuth{}, 2*time.Second).Send(context.Background(), []byte(`{}`))
	if err != nil || res.Status != http.StatusConflict {
		t.Fatalf("отказ planning: %+v, %v", res, err)
	}
}
