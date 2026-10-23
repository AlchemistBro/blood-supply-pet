package runtime

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"testing/fstest"

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
