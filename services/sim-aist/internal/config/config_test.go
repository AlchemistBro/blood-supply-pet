package config_test

import (
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/services/sim-aist/internal/config"
)

func TestDefaults(t *testing.T) {
	for _, k := range []string{"BS_HTTP_ADDR", "BS_SIM_SEED_ON_START", "BS_SIM_SEED", "BS_SIM_SEED_UNITS", "BS_LOG_LEVEL"} {
		t.Setenv(k, "")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":8201" || !cfg.SeedOnStart || cfg.SeedUnits <= 0 || cfg.Seed == 0 {
		t.Fatalf("значения по умолчанию: %+v", cfg)
	}
}

func TestOverrides(t *testing.T) {
	t.Setenv("BS_HTTP_ADDR", ":9999")
	t.Setenv("BS_SIM_SEED_ON_START", "false")
	t.Setenv("BS_SIM_SEED", "7")
	t.Setenv("BS_SIM_SEED_UNITS", "10")
	t.Setenv("BS_LOG_LEVEL", "debug")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":9999" || cfg.SeedOnStart || cfg.Seed != 7 || cfg.SeedUnits != 10 || cfg.LogLevel != "debug" {
		t.Fatalf("переопределение: %+v", cfg)
	}
	if config.NewLogger(cfg) == nil {
		t.Fatal("нет логгера")
	}
}

func TestInvalid(t *testing.T) {
	t.Setenv("BS_SIM_SEED_UNITS", "-1")
	if _, err := config.Load(); err == nil {
		t.Fatal("отрицательное число единиц должно отвергаться")
	}
}
