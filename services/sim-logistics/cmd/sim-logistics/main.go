// sim-logistics — имитатор перевозчика C03 для тестов и демо: заказы перевозки без дедупликации,
// сверка по clientRef, отмена, синтетические оценки доставки и пульт сбоев. Состояние в памяти,
// без базы и без JWT: имитатор не часть системы. Порт по умолчанию 8203.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/caarlos0/env/v11"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/observability"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	bsruntime "github.com/AlchemistBro/blood-supply-pet/libs/runtime"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-logistics/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-logistics/internal/carrier"
)

const serviceName = "sim-logistics"

// config — переменные окружения имитатора (префикс BS_, как у сервисов).
type config struct {
	HTTPAddr     string `env:"BS_HTTP_ADDR" envDefault:":8203"`
	OTLPEndpoint string `env:"BS_OTEL_ENDPOINT"`
	LogLevel     string `env:"BS_LOG_LEVEL" envDefault:"info"`
	LogFormat    string `env:"BS_LOG_FORMAT" envDefault:"json"`
	Environment  string `env:"BS_ENV" envDefault:"local"`
}

func main() {
	os.Exit(runMain())
}

// runMain отделён от main, чтобы отложенные вызовы выполнились до os.Exit.
func runMain() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("имитатор остановлен с ошибкой", "service", serviceName, "err", err)
		return 1
	}
	return 0
}

func run(ctx context.Context) error {
	var cfg config
	if err := env.Parse(&cfg); err != nil {
		return fmt.Errorf("конфигурация: %w", err)
	}
	obs, err := observability.Init(ctx, observability.Config{
		ServiceName: serviceName, Version: bsruntime.Version, Environment: cfg.Environment,
		OTLPEndpoint: cfg.OTLPEndpoint, LogLevel: cfg.LogLevel, LogFormat: cfg.LogFormat,
	})
	if err != nil {
		return fmt.Errorf("наблюдаемость: %w", err)
	}
	defer func() { _ = obs.Shutdown(context.Background()) }()
	log := obs.Logger

	ref, err := refdata.Load()
	if err != nil {
		return fmt.Errorf("справочники: %w", err)
	}
	clinics := make([]carrier.Clinic, 0, len(ref.Clinics))
	for _, c := range ref.Clinics {
		if c.IsActive {
			clinics = append(clinics, carrier.Clinic{ClinicID: c.ClinicID, DistrictID: c.DistrictID})
		}
	}

	clk := clock.System{}
	handler := api.NewHandler(api.Deps{
		Carrier:   carrier.New(clk),
		Estimator: carrier.NewEstimator(clinics, clk),
		Log:       log,
		Metrics:   obs.MetricsHandler,
	})
	log.Info("запуск имитатора C03", "env", cfg.Environment, "addr", cfg.HTTPAddr, "clinics", len(clinics))
	if err := httpx.NewServer(cfg.HTTPAddr, handler, log).Run(ctx); err != nil {
		return fmt.Errorf("HTTP-сервер: %w", err)
	}
	return nil
}
