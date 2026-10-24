package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/domain"
)

// callsPerDrive — сколько вызовов C03 делает один drive в худшем случае: сверка и отправка или отмена.
// С запасом на третий: аренда должна пережить все вызовы, иначе исход запишет уже не владелец.
const callsPerDrive = 3

// config — переменные шлюза (префикс BS_, ADR-0015). Значения по умолчанию — из прототипа (application.yaml).
type config struct {
	C03URL            string        `env:"BS_C03_URL" envDefault:"http://localhost:8203"`
	C03Timeout        time.Duration `env:"BS_C03_TIMEOUT" envDefault:"5s"`
	Lease             time.Duration `env:"BS_LOGISTICS_LEASE" envDefault:"30s"`
	MaxAttempts       int           `env:"BS_LOGISTICS_MAX_ATTEMPTS" envDefault:"5"`
	Backoff           time.Duration `env:"BS_LOGISTICS_BACKOFF" envDefault:"1s"`
	UnknownRecheck    time.Duration `env:"BS_LOGISTICS_UNKNOWN_RECHECK" envDefault:"1m"`
	ReconcileInterval time.Duration `env:"BS_LOGISTICS_RECONCILE_INTERVAL" envDefault:"1s"`
	ReconcileBatch    int           `env:"BS_LOGISTICS_RECONCILE_BATCH" envDefault:"100"`
	ReconcilerEnabled bool          `env:"BS_LOGISTICS_RECONCILER_ENABLED" envDefault:"true"`
}

func loadConfig() (config, error) {
	var c config
	if err := env.Parse(&c); err != nil {
		return config{}, fmt.Errorf("конфигурация шлюза: %w", err)
	}
	if err := c.validate(); err != nil {
		return config{}, err
	}
	return c, nil
}

func (c config) validate() error {
	switch {
	case c.C03URL == "":
		return errors.New("BS_C03_URL не задан")
	case c.C03Timeout <= 0 || c.Lease <= 0 || c.Backoff <= 0 || c.UnknownRecheck <= 0 || c.ReconcileInterval <= 0:
		return errors.New("таймаут C03, аренда, пауза, перепроверка и период сверщика должны быть положительными")
	case c.C03Timeout*callsPerDrive >= c.Lease:
		return fmt.Errorf("BS_C03_TIMEOUT (%v) × %d должен быть меньше BS_LOGISTICS_LEASE (%v)", c.C03Timeout, callsPerDrive, c.Lease)
	case c.MaxAttempts < 1:
		return errors.New("BS_LOGISTICS_MAX_ATTEMPTS должен быть не меньше 1")
	case c.ReconcileBatch < 1:
		return errors.New("BS_LOGISTICS_RECONCILE_BATCH должен быть не меньше 1")
	default:
		return nil
	}
}

func (c config) settings() app.Settings {
	return app.Settings{
		Lease:             c.Lease,
		CallTimeout:       c.C03Timeout,
		Retry:             domain.RetryPolicy{MaxAttempts: c.MaxAttempts, Backoff: c.Backoff, UnknownRecheck: c.UnknownRecheck},
		ReconcileBatch:    c.ReconcileBatch,
		ReconcileInterval: c.ReconcileInterval,
	}
}
