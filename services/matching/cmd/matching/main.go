// matching - сервис АС распределения компонентов донорской крови.
package main

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/libs/k1seal/opener"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"

	"github.com/AlchemistBro/blood-supply-pet/libs/runtime"
	apispec "github.com/AlchemistBro/blood-supply-pet/services/matching/api"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/migrations"
)

func main() {
	runtime.Run(runtime.Spec{
		Name:       "matching",
		Module:     "matching",
		Migrations: migrations.FS,
		OpenAPI:    apispec.Spec,
		Setup: func(ctx context.Context, rt *runtime.Runtime) ([]runtime.Worker, error) {
			keys, err := opener.KeyringFromEnv()
			if err != nil {
				return nil, err
			}
			svc := app.New(rt.Pool, postgres.New(rt.Pool), rt.Journal, rt.Refdata, rt.Clock, keys)
			if err := svc.SeedRules(ctx); err != nil {
				return nil, err
			}
			denied := rt.Deny()
			api.Mount(rt.Mux, svc, func(r *http.Request, p *auth.Principal, op auth.Operation) {
				if op != auth.OpReadPatientCase {
					denied(r, p, op)
					return
				}
				id, _ := uuid.Parse(r.PathValue("id"))
				if err := svc.RecordReadDenied(r.Context(), id, api.Actor(r.Context())); err != nil {
					rt.Log.ErrorContext(r.Context(), "не удалось записать отказ чтения номера случая", "err", err)
				}
			})
			if len(rt.Cfg.KafkaBrokers) == 0 {
				return nil, nil
			}
			topics := []string{contracts.TopicInventoryEvents, contracts.TopicLogisticsEvents, contracts.TopicTransferEvents}
			if err := messaging.EnsureTopics(ctx, rt.Cfg.KafkaBrokers, 1, messaging.WithDLT(topics...)...); err != nil {
				return nil, err
			}
			consumer, err := messaging.NewConsumer(messaging.ConsumerConfig{Brokers: rt.Cfg.KafkaBrokers, Group: "matching", Topics: topics}, svc.Handle, rt.Log)
			if err != nil {
				return nil, err
			}
			return []runtime.Worker{consumer.Run}, nil
		},
	})
}
