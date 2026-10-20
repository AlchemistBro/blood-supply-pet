// audit — неизменяемый журнал аудита: читает audit.events, принимает записи от web, проверяет цепочку хешей.
package main

import (
	"context"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	"github.com/AlchemistBro/blood-supply-pet/libs/runtime"
	apispec "github.com/AlchemistBro/blood-supply-pet/services/audit/api"
	"github.com/AlchemistBro/blood-supply-pet/services/audit/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/audit/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/services/audit/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/audit/migrations"
)

func main() {
	runtime.Run(runtime.Spec{
		Name:       "audit",
		Module:     auditc.ModuleAudit,
		Migrations: migrations.FS,
		OpenAPI:    apispec.Spec,
		Setup: func(ctx context.Context, rt *runtime.Runtime) ([]runtime.Worker, error) {
			svc := app.New(rt.Pool, postgres.New(rt.Pool), rt.Clock, rt.Log)
			api.Mount(rt.Mux, svc, rt.Deny())
			var workers []runtime.Worker
			if len(rt.Cfg.KafkaBrokers) > 0 {
				if err := messaging.EnsureTopics(ctx, rt.Cfg.KafkaBrokers, 1, messaging.WithDLT(contracts.TopicAuditEvents)...); err != nil {
					return nil, err
				}
				consumer, err := messaging.NewConsumer(messaging.ConsumerConfig{
					Brokers: rt.Cfg.KafkaBrokers, Group: "audit", Topics: []string{contracts.TopicAuditEvents},
				}, svc.HandleMessage, rt.Log)
				if err != nil {
					return nil, err
				}
				workers = append(workers, consumer.Run)
			}
			return workers, nil
		},
	})
}
