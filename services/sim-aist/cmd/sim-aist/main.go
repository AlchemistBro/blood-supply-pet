// sim-aist — имитатор АИСТ-М (C01) для тестов и демо: единицы в памяти, ИНТ-01 и ИНТ-05, управляемые сбои.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testdata"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-aist/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-aist/internal/config"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-aist/internal/state"
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load()
	if err != nil {
		config.NewLogger(config.Config{}).Error("старт имитатора", "err", err)
		return 1
	}
	log := config.NewLogger(cfg)
	ref, err := refdata.Load()
	if err != nil {
		log.Error("справочники", "err", err)
		return 1
	}
	clk := clock.System{}
	store := state.New(clk, ref)
	seed := api.SeedDefaults{Seed: cfg.Seed, Units: cfg.SeedUnits, WithdrawnShare: cfg.SeedWithdrawnShare}
	if cfg.SeedOnStart {
		created, _, err := store.Seed(testdata.Generate(ref, api.SeedConfig(seed, clk.Now())).Units)
		if err != nil {
			log.Error("стартовый набор", "err", err)
			return 1
		}
		log.Info("стартовый набор загружен", "seed", cfg.Seed, "units", created)
	}
	handler := api.New(store, ref, clk, log, api.Options{Seed: seed}).Routes()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := httpx.NewServer(cfg.HTTPAddr, handler, log).Run(ctx); err != nil {
		log.Error("имитатор остановлен с ошибкой", "err", err)
		return 1
	}
	return 0
}
