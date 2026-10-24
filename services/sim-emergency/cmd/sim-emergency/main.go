// sim-emergency — имитатор C02 «Моделирование ЧС» для тестов и демо: формирует синтетический
// агрегированный прогноз ЧС и отправляет его в planning по ИНТ-02. Без базы и без проверки JWT
// на входе: имитатор не часть системы. Порт по умолчанию 8202.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-emergency/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-emergency/internal/config"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-emergency/internal/sender"
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
	var tokens sender.TokenSource = sender.Unconfigured{}
	switch {
	case cfg.NoAuth:
		tokens = sender.NoAuth{}
		log.Warn("прогноз отправляется без служебного токена (BS_SIM_NO_AUTH): только для локальной разработки")
	case cfg.ServiceTokenURL != "":
		tokens, err = auth.NewServiceToken(auth.ClientCredentialsConfig{TokenURL: cfg.ServiceTokenURL, ClientID: cfg.ServiceClientID, ClientSecret: cfg.ServiceClientSecret, Clock: clock.System{}})
		if err != nil {
			log.Error("конфигурация служебного токена C02", "err", err)
			return 1
		}
	default:
		log.Warn("служебный токен не настроен: отправка прогноза вернёт 503; для локальной разработки BS_SIM_NO_AUTH=true")
	}
	snd := sender.New(cfg.PlanningURL, tokens, cfg.SendTimeout)
	handler := api.NewHandler(api.Deps{
		Ref: ref, Clock: clock.System{}, Sender: snd, Log: log, DefaultSeed: cfg.Seed,
	})
	log.Info("запуск имитатора C02", "addr", cfg.HTTPAddr, "planning", snd.URL())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := httpx.NewServer(cfg.HTTPAddr, handler, log).Run(ctx); err != nil {
		log.Error("имитатор остановлен с ошибкой", "err", err)
		return 1
	}
	return 0
}
