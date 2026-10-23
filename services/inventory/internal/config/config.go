// Package config — собственные настройки inventory из переменных окружения (префикс BS_);
// общие (база, Kafka, OIDC, BS_ENV) читает libs/runtime.
package config

import (
	"errors"
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config — связь с АИСТ-М (ИНТ-01).
type Config struct {
	// AistURL — адрес АИСТ-М (на стенде — имитатор sim-aist).
	AistURL string `env:"BS_AIST_URL" envDefault:"http://localhost:8201"`
	// PollInterval — период опроса изменений.
	PollInterval time.Duration `env:"BS_AIST_POLL_INTERVAL" envDefault:"5s"`
	// PageLimit — размер страницы изменений (АИСТ-М принимает от 1 до 5000).
	PageLimit int `env:"BS_AIST_PAGE_LIMIT" envDefault:"500"`
	// StatusTimeout — предел вызова АИСТ-М в точечном статусе; меньше 3 с бюджета transfer → inventory
	// (ADR-0004): остаток нужен на ответ из копии.
	StatusTimeout time.Duration `env:"BS_AIST_STATUS_TIMEOUT" envDefault:"2s"`
}

// Load читает и проверяет настройки.
func Load() (Config, error) {
	var c Config
	if err := env.Parse(&c); err != nil {
		return Config{}, fmt.Errorf("конфигурация inventory: %w", err)
	}
	switch {
	case c.AistURL == "":
		return Config{}, errors.New("конфигурация inventory: BS_AIST_URL не задан")
	case c.PollInterval <= 0:
		return Config{}, errors.New("конфигурация inventory: BS_AIST_POLL_INTERVAL должен быть положительным")
	case c.PageLimit < 1 || c.PageLimit > 5000:
		return Config{}, errors.New("конфигурация inventory: BS_AIST_PAGE_LIMIT — от 1 до 5000")
	case c.StatusTimeout <= 0:
		return Config{}, errors.New("конфигурация inventory: BS_AIST_STATUS_TIMEOUT должен быть положительным")
	}
	return c, nil
}
