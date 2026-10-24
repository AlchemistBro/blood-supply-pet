package testinfra

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestDatabaseURLPreservesCredentialsAndOptions(t *testing.T) {
	original := "postgresql://user:p%40ss%2Fword@example.invalid:5432/application?sslmode=require&application_name=checks"
	result, err := replaceDatabase(original, "bs_test_new")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(result)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if u.Path != "/bs_test_new" || u.Host != "example.invalid:5432" || password != "p@ss/word" || u.Query().Get("sslmode") != "require" || u.Query().Get("application_name") != "checks" {
		t.Fatal("замена имени базы изменила параметры соединения")
	}
	for _, invalid := range []string{"", "host=example.invalid", "https://example.invalid/db", "postgres://u:%secret@host/db"} {
		if _, err := replaceDatabase(invalid, "bs_test_new"); err == nil {
			t.Fatal("принят неверный URL")
		}
	}
}

func TestDatabaseFailureDoesNotExposeConnectionString(t *testing.T) {
	if strings.Contains(databaseFailure(errors.New("postgres://user:secret@host/db")), "secret") {
		t.Fatal("пароль соединения попал в диагностику")
	}
	if got := databaseFailure(&pgconn.PgError{Code: "42501", Message: "permission denied"}); !strings.Contains(got, "42501") {
		t.Fatal("потерян SQLSTATE ошибки сервера")
	}
}

func TestNoDockerFlagFailsClosedForUnexpectedValues(t *testing.T) {
	for _, value := range []string{"true", "TRUE", "1", " true ", "typo"} {
		t.Setenv("BS_TEST_NO_DOCKER", value)
		if !noDocker() {
			t.Fatalf("значение %q не запретило Docker", value)
		}
	}
	for _, value := range []string{"", "false", "FALSE", "0"} {
		t.Setenv("BS_TEST_NO_DOCKER", value)
		if noDocker() {
			t.Fatalf("значение %q не отключило флаг", value)
		}
	}
}
