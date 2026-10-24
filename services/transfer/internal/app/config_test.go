package app_test

import (
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
)

// Экстренное подтверждение по копии выключено по умолчанию (допущение этапа 1), таймер — каждые 5 с.
func TestConfigDefaults(t *testing.T) {
	t.Setenv("BS_ALLOW_COPY_CONFIRM_EMERGENCY", "")
	t.Setenv("BS_EXPIRY_INTERVAL", "")
	cfg, err := app.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AllowCopyConfirmEmergency {
		t.Error("BS_ALLOW_COPY_CONFIRM_EMERGENCY по умолчанию должен быть false")
	}
	if cfg.ExpiryInterval != 5*time.Second {
		t.Errorf("период таймера %s, ожидали 5s", cfg.ExpiryInterval)
	}
}

func TestConfigFromEnvironment(t *testing.T) {
	t.Setenv("BS_ALLOW_COPY_CONFIRM_EMERGENCY", "true")
	t.Setenv("BS_EXPIRY_INTERVAL", "1s")
	cfg, err := app.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AllowCopyConfirmEmergency || cfg.ExpiryInterval != time.Second {
		t.Errorf("переменные окружения не прочитаны: %+v", cfg)
	}
}
