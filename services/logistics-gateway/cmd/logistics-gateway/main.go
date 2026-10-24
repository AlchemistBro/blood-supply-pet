// logistics-gateway — шлюз перевозчика C03 (ИНТ-03): единственный, кто говорит с перевозчиком. Принимает
// RequestTransport и CancelTransport из logistics.commands, отвечает в logistics.replies. «Ровно один заказ»
// при дублях, обрывах, падениях и нескольких экземплярах держат первичный ключ, аренда строки и сверка.
package main

import (
	"context"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	"github.com/AlchemistBro/blood-supply-pet/libs/runtime"
	apispec "github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/api"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/adapters/c03"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/adapters/kafka"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/migrations"
)

func main() {
	runtime.Run(runtime.Spec{
		Name:       "logistics-gateway",
		Module:     auditc.ModuleLogistics,
		Migrations: migrations.FS,
		OpenAPI:    apispec.Spec,
		Setup: func(ctx context.Context, rt *runtime.Runtime) ([]runtime.Worker, error) {
			cfg, err := loadConfig()
			if err != nil {
				return nil, err
			}
			carrier := c03.New(cfg.C03URL, cfg.C03Timeout)
			estimates := app.Estimates{Pool: rt.Pool, Source: carrier, Journal: rt.Journal, Ref: rt.Refdata, Log: rt.Log}
			gw := app.New(rt.Pool, postgres.New(rt.Pool), carrier,
				kafka.Outbox{Journal: rt.Journal, Topic: contracts.TopicLogisticsReplies}, rt.Clock, rt.Log, cfg.settings())
			progress := app.ProgressFeed{Store: postgres.ProgressStore{Pool: rt.Pool, Journal: rt.Journal}, Source: carrier, Clock: rt.Clock, Log: rt.Log}
			workers := []runtime.Worker{estimates.Run, progress.Run}
			if cfg.ReconcilerEnabled {
				workers = append(workers, gw.RunReconciler)
			}
			if len(rt.Cfg.KafkaBrokers) > 0 {
				topics := append(messaging.WithDLT(contracts.TopicLogisticsCommands), contracts.TopicLogisticsReplies, contracts.TopicLogisticsEvents)
				if err := messaging.EnsureTopics(ctx, rt.Cfg.KafkaBrokers, 1, topics...); err != nil {
					return nil, err
				}
				consumer, err := messaging.NewConsumer(messaging.ConsumerConfig{
					Brokers: rt.Cfg.KafkaBrokers, Group: "logistics-gateway",
					Topics: []string{contracts.TopicLogisticsCommands},
				}, estimates.Handle(kafka.Handler(gw)), rt.Log)
				if err != nil {
					return nil, err
				}
				workers = append(workers, consumer.Run)
			}
			return workers, nil
		},
	})
}
