// Package config — настройки имитатора АИСТ-М из переменных окружения (префикс BS_).
// Имитатор не использует libs/runtime: базы, Kafka и JWT у него нет.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/caarlos0/env/v11"
)

// Config — переменные окружения имитатора.
type Config struct {
	HTTPAddr  string `env:"BS_HTTP_ADDR" envDefault:":8201"`
	LogLevel  string `env:"BS_LOG_LEVEL" envDefault:"info"`
	LogFormat string `env:"BS_LOG_FORMAT" envDefault:"json"`
	// SeedOnStart — загрузить набор из libs/testdata при старте.
	SeedOnStart bool  `env:"BS_SIM_SEED_ON_START" envDefault:"true"`
	Seed        int64 `env:"BS_SIM_SEED" envDefault:"20261020"`
	// SeedUnits — размер набора при старте и по умолчанию для POST /dev/seed. Демо-стенду хватает
	// нескольких тысяч; 100 000 из NFR002 задаются явно для бенчмарка.
	SeedUnits          int     `env:"BS_SIM_SEED_UNITS" envDefault:"2000"`
	SeedWithdrawnShare float64 `env:"BS_SIM_SEED_WITHDRAWN_SHARE" envDefault:"0.05"`
}

// Load читает и проверяет конфигурацию.
func Load() (Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, fmt.Errorf("конфигурация: %w", err)
	}
	if cfg.SeedUnits < 0 {
		return Config{}, errors.New("конфигурация: BS_SIM_SEED_UNITS не может быть отрицательным")
	}
	if cfg.SeedWithdrawnShare < 0 || cfg.SeedWithdrawnShare > 1 {
		return Config{}, errors.New("конфигурация: BS_SIM_SEED_WITHDRAWN_SHARE — доля от 0 до 1")
	}
	return cfg, nil
}

// NewLogger — структурный логгер в stdout: JSON для стенда, текст для локальной отладки.
func NewLogger(cfg Config) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler = slog.NewJSONHandler(os.Stdout, opts)
	if cfg.LogFormat == "text" {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(h).With("service", "sim-aist")
}
