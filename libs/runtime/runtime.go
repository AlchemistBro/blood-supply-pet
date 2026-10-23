// Package runtime — единый запуск сервиса: конфигурация из переменных окружения, наблюдаемость,
// подключение к PostgreSQL (кроме сервисов со Spec.NoDB), миграции (платформа + справочники + свои), сид справочников, проверка JWT,
// служебные маршруты (/healthz, /readyz, /metrics, /openapi.yaml), ретранслятор outbox, фоновые воркеры,
// мягкая остановка по сигналу. Сервису остаётся собрать зависимости и зарегистрировать маршруты.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	"github.com/AlchemistBro/blood-supply-pet/libs/observability"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
)

// Version подставляется при сборке: -ldflags "-X .../libs/runtime.Version=v1.2.3".
var Version = "dev"

// Config — общие переменные окружения всех сервисов (префикс BS_).
type Config struct {
	HTTPAddr string `env:"BS_HTTP_ADDR" envDefault:":8080"`
	// DBURL обязателен для сервиса с базой (проверяет checkConfig); сервису со Spec.NoDB не нужен.
	DBURL        string   `env:"BS_DB_URL"`
	KafkaBrokers []string `env:"BS_KAFKA_BROKERS" envSeparator:","`
	OIDCIssuer   string   `env:"BS_OIDC_ISSUER"`
	OIDCAudience string   `env:"BS_OIDC_AUDIENCE"`
	OIDCJWKSURL  string   `env:"BS_OIDC_JWKS_URL"`
	OTLPEndpoint string   `env:"BS_OTEL_ENDPOINT"`
	LogLevel     string   `env:"BS_LOG_LEVEL" envDefault:"info"`
	LogFormat    string   `env:"BS_LOG_FORMAT" envDefault:"json"`
	Environment  string   `env:"BS_ENV" envDefault:"local"`
	// AuthDisabled — только для локальной отладки без Keycloak; в compose и k8s всегда false.
	AuthDisabled bool `env:"BS_AUTH_DISABLED" envDefault:"false"`
	// RelayInterval — период опроса outbox.
	RelayInterval time.Duration `env:"BS_RELAY_INTERVAL" envDefault:"200ms"`
}

// Spec — что сервис сообщает каркасу о себе.
type Spec struct {
	// Name — имя сервиса (producer в заголовках, service.name в телеметрии).
	Name string
	// Module — модуль аудита (contracts/audit.Module*).
	Module string
	// Migrations — собственные миграции сервиса (00100+).
	Migrations fs.FS
	// NoDB — сервис без базы (имитаторы смежных систем): нет подключения к PostgreSQL, миграций,
	// справочников в базе, outbox и ретранслятора; Runtime.Pool и Runtime.Journal равны nil.
	NoDB bool
	// OpenAPI — текст контракта для GET /openapi.yaml (может быть nil).
	OpenAPI []byte
	// Setup собирает зависимости, регистрирует маршруты на rt.Mux и возвращает фоновые воркеры.
	Setup func(ctx context.Context, rt *Runtime) ([]Worker, error)
}

// Worker — фоновая работа, живущая до отмены контекста (consumer, таймер).
type Worker func(ctx context.Context) error

// Runtime — собранные зависимости.
type Runtime struct {
	Cfg      Config
	Log      *slog.Logger
	Pool     *pgxpool.Pool
	Clock    clock.Clock
	Journal  *messaging.Journal
	Verifier auth.Verifier
	Health   *httpx.Health
	Mux      *http.ServeMux
	Obs      *observability.Observability
	Refdata  *refdata.Set
	Name     string
}

// Run запускает сервис и блокируется до SIGINT/SIGTERM. Ошибка инициализации завершает процесс.
func Run(spec Spec) {
	os.Exit(runMain(spec))
}

// runMain отделён от Run, чтобы отложенные вызовы (остановка обработки сигналов) успели выполниться до os.Exit.
func runMain(spec Spec) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, spec); err != nil {
		slog.Error("сервис остановлен с ошибкой", "service", spec.Name, "err", err)
		return 1
	}
	return 0
}

func run(ctx context.Context, spec Spec) error {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return fmt.Errorf("конфигурация: %w", err)
	}
	if err := checkConfig(cfg, spec); err != nil {
		return err
	}
	obs, err := observability.Init(ctx, observability.Config{
		ServiceName: spec.Name, Version: Version, Environment: cfg.Environment,
		OTLPEndpoint: cfg.OTLPEndpoint, LogLevel: cfg.LogLevel, LogFormat: cfg.LogFormat,
	})
	if err != nil {
		return err
	}
	defer func() { _ = obs.Shutdown(context.Background()) }()
	log := obs.Logger
	log.Info("запуск", "env", cfg.Environment, "addr", cfg.HTTPAddr)

	ref := refdata.MustLoad()
	rt := &Runtime{
		Cfg: cfg, Log: log, Clock: clock.System{}, Health: httpx.NewHealth(),
		Mux: http.NewServeMux(), Obs: obs, Refdata: ref, Name: spec.Name,
	}
	if !spec.NoDB {
		pool, err := openDatabase(ctx, cfg, spec, ref, log)
		if err != nil {
			return err
		}
		defer pool.Close()
		rt.Pool = pool
		rt.Journal = messaging.NewJournal(spec.Name, spec.Module, clock.System{})
		rt.Health.AddReadiness("postgres", func(ctx context.Context) error { return pool.Ping(ctx) })
	}
	if len(cfg.KafkaBrokers) > 0 {
		rt.Health.AddReadiness("kafka", func(ctx context.Context) error { return messaging.Ping(ctx, cfg.KafkaBrokers) })
	}

	if !cfg.AuthDisabled {
		if cfg.OIDCIssuer == "" {
			return errors.New("BS_OIDC_ISSUER не задан (или BS_AUTH_DISABLED=true для локальной отладки)")
		}
		verifier, err := newVerifierWithRetry(ctx, log, auth.Config{Issuer: cfg.OIDCIssuer, Audience: cfg.OIDCAudience, JWKSURL: cfg.OIDCJWKSURL})
		if err != nil {
			return err
		}
		rt.Verifier = verifier
	} else {
		log.Warn("проверка JWT отключена (BS_AUTH_DISABLED=true): только для локальной отладки")
		rt.Verifier = devVerifier{}
	}

	rt.Health.Register(rt.Mux)
	rt.Mux.Handle("GET /metrics", obs.MetricsHandler)
	if spec.OpenAPI != nil {
		rt.Mux.HandleFunc("GET /openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/yaml")
			_, _ = w.Write(spec.OpenAPI)
		})
	}

	workers, err := spec.Setup(ctx, rt)
	if err != nil {
		return fmt.Errorf("инициализация %s: %w", spec.Name, err)
	}

	handler := httpx.Chain(rt.Mux, append(httpx.Standard(log), observability.HTTPMiddleware(spec.Name), auth.Authenticate(rt.Verifier))...)
	server := httpx.NewServer(cfg.HTTPAddr, handler, log)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return server.Run(gctx) })
	// Ретранслятор outbox нужен только сервису с базой: без базы нет и outbox.
	if len(cfg.KafkaBrokers) > 0 && rt.Pool != nil {
		producer, err := messaging.NewProducerClient(cfg.KafkaBrokers)
		if err != nil {
			return err
		}
		defer producer.Close()
		relayCfg := messaging.DefaultRelayConfig()
		relayCfg.Interval = cfg.RelayInterval
		relay := messaging.NewRelay(rt.Pool, messaging.KafkaProducer{Client: producer}, relayCfg, log)
		g.Go(func() error { return relay.Run(gctx) })
	}
	for _, w := range workers {
		g.Go(func() error { return w(gctx) })
	}
	log.Info("сервис готов", "service", spec.Name)
	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("сервис остановлен", "service", spec.Name)
	return nil
}

// checkConfig проверяет согласованность конфигурации и описания сервиса до любых подключений.
func checkConfig(cfg Config, spec Spec) error {
	if spec.NoDB {
		if spec.Migrations != nil {
			return fmt.Errorf("%s: сервис без базы (NoDB) не может иметь миграций", spec.Name)
		}
		return nil
	}
	if cfg.DBURL == "" {
		return errors.New("BS_DB_URL не задан (сервису без базы нужен Spec.NoDB)")
	}
	return nil
}

// openDatabase подключается к базе сервиса, применяет миграции (платформа, справочники, свои)
// и сидирует справочники.
func openDatabase(ctx context.Context, cfg Config, spec Spec, ref *refdata.Set, log *slog.Logger) (*pgxpool.Pool, error) {
	pool, err := bspgx.Connect(ctx, cfg.DBURL)
	if err != nil {
		return nil, err
	}
	sources := []fs.FS{messaging.Migrations, refdata.Migrations}
	if spec.Migrations != nil {
		sources = append(sources, spec.Migrations)
	}
	applied, err := bspgx.Migrate(ctx, pool, sources...)
	if err != nil {
		pool.Close()
		return nil, err
	}
	if len(applied) > 0 {
		log.Info("миграции применены", "files", applied)
	}
	if err := bspgx.WithTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error { return ref.Seed(ctx, tx) }); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// newVerifierWithRetry ждёт Keycloak при старте стенда: discovery повторяется до минуты.
func newVerifierWithRetry(ctx context.Context, log *slog.Logger, cfg auth.Config) (auth.Verifier, error) {
	deadline := time.Now().Add(90 * time.Second)
	for {
		v, err := auth.NewVerifier(ctx, cfg)
		if err == nil {
			return v, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, err
		}
		log.Warn("Keycloak пока недоступен, повтор через 3 с", "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// devVerifier принимает любой токен вида "dev:<username>:<ROLE>[:<clinic_id>]" — только для локальной отладки.
type devVerifier struct{}

func (devVerifier) Verify(_ context.Context, raw string) (auth.Principal, error) {
	var user, role string
	var clinic int64
	n, _ := fmt.Sscanf(raw, "dev:%s", &user)
	if n == 0 {
		return auth.Principal{}, auth.ErrUnauthorized
	}
	parts := splitColon(raw)
	if len(parts) < 3 {
		return auth.Principal{}, auth.ErrUnauthorized
	}
	user, role = parts[1], parts[2]
	p := auth.Principal{Subject: user, Username: user, Roles: []auth.Role{auth.Role(role)}}
	if len(parts) > 3 {
		if _, err := fmt.Sscanf(parts[3], "%d", &clinic); err == nil {
			p.ClinicID = &clinic
		}
	}
	return p, nil
}

func splitColon(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// Deny — стандартная реакция на отказ в доступе: запись ACCESS_DENIED в аудит через journal.
func (rt *Runtime) Deny() auth.Denied {
	return func(r *http.Request, p *auth.Principal, op auth.Operation) {
		if rt.Pool == nil {
			// Сервис без базы: outbox нет, отказ фиксируется только в журнале процесса.
			rt.Log.WarnContext(r.Context(), "доступ запрещён", "operation", string(op))
			return
		}
		entry := messaging.AuditEntry{Action: "ACCESS_DENIED", ObjectType: "operation", ObjectID: string(op), Result: "DENIED"}
		if p != nil {
			entry.UserID, entry.Role, entry.ClinicID = p.Username, p.PrimaryRole(), p.ClinicID
		}
		err := bspgx.WithTx(r.Context(), rt.Pool, func(ctx context.Context, tx pgx.Tx) error {
			return rt.Journal.Audit(ctx, tx, entry)
		})
		if err != nil {
			rt.Log.ErrorContext(r.Context(), "не удалось записать отказ в аудит", "err", err)
		}
	}
}
