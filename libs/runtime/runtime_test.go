package runtime

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
)

// Сервису с базой адрес базы обязателен; сервису без базы (имитатор) — нет, и миграций у него быть не может.
func TestCheckConfig(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		spec    Spec
		wantErr bool
	}{
		{"с базой и адресом", Config{DBURL: "postgres://x"}, Spec{Name: "audit"}, false},
		{"с базой без адреса", Config{}, Spec{Name: "audit"}, true},
		{"без базы", Config{}, Spec{Name: "sim-aist", NoDB: true}, false},
		{"без базы, но с миграциями", Config{}, Spec{Name: "sim-aist", NoDB: true, Migrations: fstest.MapFS{}}, true},
	}
	for _, c := range cases {
		if err := checkConfig(c.cfg, c.spec); (err != nil) != c.wantErr {
			t.Errorf("%s: err=%v, ожидали ошибку=%v", c.name, err, c.wantErr)
		}
	}
}

// Без базы отказ в доступе не пишется в аудит (outbox нет), но и не роняет обработку запроса.
func TestDenyWithoutDatabaseDoesNotPanic(t *testing.T) {
	rt := &Runtime{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Name: "sim"}
	req := httptest.NewRequest("GET", "/x", nil)
	rt.Deny()(req, &auth.Principal{Username: "u", Roles: []auth.Role{auth.RoleOperator}}, auth.OpViewStock)
}

// Без базы отказ уходит в OnDeny сервиса (web — POST /records в audit, ADR-0017).
func TestDenyWithoutDatabaseCallsOnDeny(t *testing.T) {
	var gotOp auth.Operation
	var gotUser string
	rt := &Runtime{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Name: "web",
		OnDeny: func(_ *http.Request, p *auth.Principal, op auth.Operation) { gotOp, gotUser = op, p.Username }}
	rt.Deny()(httptest.NewRequest(http.MethodGet, "/x", nil), &auth.Principal{Username: "physician1"}, auth.OpConfirmTransfer)
	if gotOp != auth.OpConfirmTransfer || gotUser != "physician1" {
		t.Fatalf("OnDeny не вызван: %q %q", gotOp, gotUser)
	}
}

// Своя цепочка (web, ADR-0018) заменяет bearer JWT; без неё при CustomAuth запуск останавливается,
// а Use без CustomAuth — ошибка конфигурации.
func TestAuthChain(t *testing.T) {
	custom := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Custom-Auth", "yes")
			next.ServeHTTP(w, r)
		})
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	rt := &Runtime{}
	if _, err := authChain(Spec{Name: "web", CustomAuth: true}, rt); err == nil {
		t.Fatal("CustomAuth без цепочки должен останавливать запуск")
	}

	rt.Use(custom)
	if _, err := authChain(Spec{Name: "svc"}, rt); err == nil {
		t.Fatal("Use без CustomAuth должен останавливать запуск")
	}

	mws, err := authChain(Spec{Name: "web", CustomAuth: true}, rt)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	httpx.Chain(ok, mws...).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/page", nil))
	if rec.Code != http.StatusNoContent || rec.Header().Get("X-Custom-Auth") != "yes" {
		t.Fatalf("без bearer-токена запрос проходит через свою цепочку: %d", rec.Code)
	}

	// По умолчанию — прежняя проверка bearer JWT: без токена 401, с токеном проходит.
	mws, err = authChain(Spec{Name: "svc"}, &Runtime{Verifier: devVerifier{}})
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	httpx.Chain(ok, mws...).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("без токена ожидался 401, получен %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer dev:u1:IT_SERVICE")
	rec = httptest.NewRecorder()
	httpx.Chain(ok, mws...).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("с токеном ожидался 204, получен %d", rec.Code)
	}
}
