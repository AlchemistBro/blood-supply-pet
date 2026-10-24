//go:build integration

package testinfra

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestExternalDatabaseIsIsolatedAndRemovedAfterTest(t *testing.T) {
	admin := os.Getenv("BS_TEST_DB_URL")
	if admin == "" {
		t.Skip("проверка внешнего PostgreSQL требует BS_TEST_DB_URL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("подключение: %s", databaseFailure(err))
	}
	defer func() { _ = conn.Close(ctx) }()
	var original, temporary string
	if err := conn.QueryRow(ctx, "SELECT current_database()").Scan(&original); err != nil {
		t.Fatal(err)
	}
	if !t.Run("temporary", func(t *testing.T) {
		address := Postgres(t)
		parsed, err := url.Parse(address)
		if err != nil {
			t.Fatal("URL временной базы не разобран")
		}
		temporary = strings.TrimPrefix(parsed.Path, "/")
		if !strings.HasPrefix(temporary, "bs_test_") || temporary == original {
			t.Fatal("проверка получила исходную базу вместо изолированной")
		}
		testConn, err := pgx.Connect(ctx, address)
		if err != nil {
			t.Fatalf("временная база: %s", databaseFailure(err))
		}
		defer func() { _ = testConn.Close(ctx) }()
		if _, err := testConn.Exec(ctx, "CREATE TABLE test_isolation_marker(id integer PRIMARY KEY)"); err != nil {
			t.Fatal(err)
		}
	}) {
		t.FailNow()
	}
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)", temporary).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("временная база осталась после cleanup")
	}
	if err := conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)", original).Scan(&exists); err != nil || !exists {
		t.Fatal("исходная база должна остаться доступной")
	}
}
