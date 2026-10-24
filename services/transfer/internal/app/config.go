package app

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config — настройки сценариев transfer (переменные окружения BS_*).
type Config struct {
	// AllowCopyConfirmEmergency — подтверждать экстренное задание по копии inventory, когда АИСТ-М не отвечает
	// (схема 12 допускает это с алертом). Допущение этапа 1: всегда false, ветка «true» не реализована —
	// решение и правила оформит отдельный ADR. Пока флаг только читается и предупреждает в логе.
	AllowCopyConfirmEmergency bool `env:"BS_ALLOW_COPY_CONFIRM_EMERGENCY" envDefault:"false"`
	// ExpiryInterval — период опроса таймера срока подтверждения.
	ExpiryInterval time.Duration `env:"BS_EXPIRY_INTERVAL" envDefault:"5s"`
	// ExpiryBatch — сколько просроченных заданий обрабатывать за один проход.
	ExpiryBatch int `env:"BS_EXPIRY_BATCH" envDefault:"100"`
}

// LoadConfig читает настройки из окружения.
func LoadConfig() (Config, error) {
	var c Config
	if err := env.Parse(&c); err != nil {
		return Config{}, fmt.Errorf("конфигурация transfer: %w", err)
	}
	return c, nil
}
