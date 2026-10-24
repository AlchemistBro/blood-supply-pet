// Package testinfra подключает внешние PostgreSQL/Kafka либо запускает Testcontainers.
// BS_TEST_NO_DOCKER запрещает запуск контейнеров. Каждая проверка получает отдельную временную базу
// с автоматическим удалением; общие контейнеры и уникальные топики не смешивают данные проверок.
package testinfra

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/testcontainers/testcontainers-go"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

var (
	pgOnce   sync.Once
	pgURL    string
	pgErr    error
	dbSeq    atomic.Int64
	kOnce    sync.Once
	kBrokers []string
	kErr     error
)

const (
	postgresImage = "postgres:16-alpine"
	kafkaImage    = "confluentinc/confluent-local:7.7.1"
)

// Postgres возвращает URL к свежей базе в общем контейнере PostgreSQL.
// Переменная BS_TEST_DB_URL переключает на внешний сервер (например, из compose).
func Postgres(t testing.TB) string {
	t.Helper()
	ctx := context.Background()
	admin := os.Getenv("BS_TEST_DB_URL")
	if admin == "" {
		if noDocker() {
			t.Fatal("BS_TEST_DB_URL не задан; запуск Docker запрещён BS_TEST_NO_DOCKER")
		}
		pgOnce.Do(func() {
			ctr, err := tcpostgres.Run(ctx, postgresImage,
				tcpostgres.WithDatabase("postgres"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"),
				testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90*time.Second)))
			if err != nil {
				pgErr = fmt.Errorf("запуск PostgreSQL: %w", err)
				return
			}
			pgURL, pgErr = ctr.ConnectionString(ctx, "sslmode=disable")
		})
		if pgErr != nil {
			t.Fatal(pgErr)
		}
		admin = pgURL
	}
	name := "bs_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	testURL, err := replaceDatabase(admin, name)
	if err != nil {
		t.Fatal("BS_TEST_DB_URL должен быть корректным postgres:// или postgresql:// URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("подключение к PostgreSQL: %s", databaseFailure(err))
	}
	defer func() { _ = conn.Close(ctx) }()
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatalf("создание временной базы: %s", databaseFailure(err))
	}
	// Зарегистрировано раньше закрытия пулов вызывающего теста; cleanup выполняется в обратном порядке.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cleanup, err := pgx.Connect(ctx, admin)
		if err != nil {
			t.Errorf("подключение для удаления %s: %s", name, databaseFailure(err))
			return
		}
		defer func() { _ = cleanup.Close(ctx) }()
		if _, err := cleanup.Exec(ctx, "DROP DATABASE "+quoted+" WITH (FORCE)"); err != nil {
			t.Errorf("удаление %s: %s", name, databaseFailure(err))
		}
	})
	return testURL
}

// Kafka возвращает адреса брокеров общего контейнера Kafka. BS_TEST_KAFKA_BROKERS переключает на внешний.
func Kafka(t testing.TB) []string {
	t.Helper()
	if env := os.Getenv("BS_TEST_KAFKA_BROKERS"); env != "" {
		return strings.Split(env, ",")
	}
	if noDocker() {
		t.Skip("Kafka не настроена; запуск Docker запрещён BS_TEST_NO_DOCKER")
	}
	kOnce.Do(func() {
		ctx := context.Background()
		ctr, err := tckafka.Run(ctx, kafkaImage, tckafka.WithClusterID("blood-supply-test"))
		if err != nil {
			kErr = fmt.Errorf("запуск Kafka: %w", err)
			return
		}
		kBrokers, kErr = ctr.Brokers(ctx)
	})
	if kErr != nil {
		t.Fatal(kErr)
	}
	return kBrokers
}

// UniqueTopic даёт уникальное имя топика для теста.
func UniqueTopic(prefix string) string {
	return fmt.Sprintf("%s.%d.%d", prefix, time.Now().UnixNano()%1_000_000_000, dbSeq.Add(1))
}

func replaceDatabase(raw, db string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" || db == "" {
		return "", errors.New("неверный URL PostgreSQL")
	}
	u.Path, u.RawPath = "/"+db, ""
	return u.String(), nil
}

func noDocker() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BS_TEST_NO_DOCKER"))) {
	case "", "false", "0":
		return false
	default:
		return true // неизвестное непустое значение не должно случайно разрешать запуск контейнеров
	}
}

// Ошибки разбора/подключения могут содержать DSN, поэтому не выводим их текст и пароль сервера.
func databaseFailure(err error) string {
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		return "SQLSTATE " + pgError.Code + ": " + pgError.Message
	}
	return fmt.Sprintf("%T", err)
}
