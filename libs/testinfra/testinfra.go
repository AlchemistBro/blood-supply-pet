// Package testinfra поднимает настоящие PostgreSQL и Kafka в Docker для интеграционных тестов
// (testcontainers-go). Контейнеры общие на тестовый процесс; каждая проверка получает свою базу
// и свои топики, чтобы тесты не мешали друг другу.
package testinfra

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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
	name := fmt.Sprintf("t%d_%d", time.Now().UnixNano()%1_000_000, dbSeq.Add(1))
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("подключение к PostgreSQL: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("создание базы %s: %v", name, err)
	}
	return replaceDatabase(admin, name)
}

// Kafka возвращает адреса брокеров общего контейнера Kafka. BS_TEST_KAFKA_BROKERS переключает на внешний.
func Kafka(t testing.TB) []string {
	t.Helper()
	if env := os.Getenv("BS_TEST_KAFKA_BROKERS"); env != "" {
		return strings.Split(env, ",")
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

func replaceDatabase(url, db string) string {
	// postgres://user:pass@host:port/dbname?params
	q := strings.Index(url, "?")
	params := ""
	base := url
	if q >= 0 {
		params = url[q:]
		base = url[:q]
	}
	slash := strings.LastIndex(base, "/")
	return base[:slash+1] + db + params
}
