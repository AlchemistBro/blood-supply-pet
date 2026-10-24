// Package config — настройки имитатора C02 из переменных окружения (префикс BS_).
// Имитатор не использует libs/runtime: базы, Kafka и проверки JWT у него нет.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config — переменные окружения имитатора.
type Config struct {
	ServiceTokenURL     string `env:"BS_SERVICE_TOKEN_URL"`
	ServiceClientID     string `env:"BS_SERVICE_CLIENT_ID" envDefault:"services"`
	ServiceClientSecret string `env:"BS_SERVICE_CLIENT_SECRET"`
	HTTPAddr            string `env:"BS_HTTP_ADDR" envDefault:":8202"`
	LogLevel            string `env:"BS_LOG_LEVEL" envDefault:"info"`
	LogFormat           string `env:"BS_LOG_FORMAT" envDefault:"json"`
	// PlanningURL — база planning или nginx (с префиксом /api); путь /int/forecast добавляется.
	PlanningURL string `env:"BS_PLANNING_URL" envDefault:"http://planning:8103"`
	// NoAuth — отправлять без служебного токена (только локальная разработка).
	NoAuth      bool          `env:"BS_SIM_NO_AUTH" envDefault:"false"`
	Seed        int64         `env:"BS_SIM_SEED" envDefault:"20261023"`
	SendTimeout time.Duration `env:"BS_SIM_SEND_TIMEOUT" envDefault:"10s"`
}

// Load читает и проверяет конфигурацию.
func Load() (Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, fmt.Errorf("конфигурация: %w", err)
	}
	u, err := url.Parse(cfg.PlanningURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Config{}, fmt.Errorf("конфигурация: BS_PLANNING_URL %q — ожидается http(s)://хост[:порт][/префикс]", cfg.PlanningURL)
	}
	if cfg.SendTimeout <= 0 {
		return Config{}, errors.New("конфигурация: BS_SIM_SEND_TIMEOUT должен быть положительным")
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
	return slog.New(h).With("service", "sim-emergency")
}
