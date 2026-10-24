// planning - сервис АС распределения компонентов донорской крови.
package main

import (
	"context"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"

	"github.com/AlchemistBro/blood-supply-pet/libs/runtime"
	apispec "github.com/AlchemistBro/blood-supply-pet/services/planning/api"
	"github.com/AlchemistBro/blood-supply-pet/services/planning/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/planning/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/services/planning/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/planning/migrations"
)

func main() {
	runtime.Run(runtime.Spec{
		Name:       "planning",
		Module:     "planning",
		Migrations: migrations.FS,
		OpenAPI:    apispec.Spec,
		Setup: func(ctx context.Context, rt *runtime.Runtime) ([]runtime.Worker, error) {
			svc := app.New(rt.Pool, postgres.New(rt.Pool), rt.Journal, rt.Refdata, rt.Clock)
			api.Mount(rt.Mux, svc, rt.Deny())
			if len(rt.Cfg.KafkaBrokers) == 0 {
				return nil, nil
			}
			topics := []string{contracts.TopicInventoryEvents, contracts.TopicDemandEvents, contracts.TopicTransferEvents, contracts.TopicLogisticsEvents, contracts.TopicMatchingEvents}
			if err := messaging.EnsureTopics(ctx, rt.Cfg.KafkaBrokers, 1, messaging.WithDLT(topics...)...); err != nil {
				return nil, err
			}
			consumer, err := messaging.NewConsumer(messaging.ConsumerConfig{Brokers: rt.Cfg.KafkaBrokers, Group: "planning", Topics: topics}, svc.Handle, rt.Log)
			if err != nil {
				return nil, err
			}
			return []runtime.Worker{consumer.Run}, nil
		},
	})
}
