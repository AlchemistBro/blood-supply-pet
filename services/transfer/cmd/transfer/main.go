// transfer — обязательства на передачу O07 и сага подтверждения (FR048, FR049; NFR004, NFR013).
// Здесь только сборка зависимостей: сценарии — internal/app, адаптеры — internal/adapters.
package main

import (
	"context"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	"github.com/AlchemistBro/blood-supply-pet/libs/runtime"
	apispec "github.com/AlchemistBro/blood-supply-pet/services/transfer/api"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/adapters/inventoryclient"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/adapters/kafka"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/migrations"
)

// consumerGroup — группа consumer-ов transfer: экземпляры делят разделы между собой.
const consumerGroup = "transfer"

func main() {
	runtime.Run(runtime.Spec{
		Name:       "transfer",
		Module:     auditc.ModuleTransfer,
		Migrations: migrations.FS,
		OpenAPI:    apispec.Spec,
		Setup:      setup,
	})
}

func setup(ctx context.Context, rt *runtime.Runtime) ([]runtime.Worker, error) {
	cfg, err := app.LoadConfig()
	if err != nil {
		return nil, err
	}
	invCfg, tokenCfg, err := inventoryclient.LoadConfig()
	if err != nil {
		return nil, err
	}
	authz, err := inventoryclient.NewAuthorizer(tokenCfg, rt.Cfg.OIDCIssuer, rt.Cfg.AuthDisabled)
	if err != nil {
		return nil, err
	}
	inventory, err := inventoryclient.New(invCfg, authz)
	if err != nil {
		return nil, err
	}
	svc := app.New(rt.Pool, postgres.New(), inventory, rt.Journal, rt.Clock, rt.Log, cfg)
	api.Mount(rt.Mux, svc, rt.Refdata, rt.Log, rt.Deny())

	workers := []runtime.Worker{svc.RunExpiryTimer}
	if len(rt.Cfg.KafkaBrokers) == 0 {
		rt.Log.Warn("BS_KAFKA_BROKERS не задан: команды, события inventory и ответы логистики не читаются")
		return workers, nil
	}
	topics := []string{contracts.TopicTransferCommands, contracts.TopicInventoryEvents, contracts.TopicLogisticsReplies,
		contracts.TopicTransferEvents, contracts.TopicLogisticsCommands, contracts.TopicAuditEvents, contracts.TopicInventoryCommands}
	if err := messaging.EnsureTopics(ctx, rt.Cfg.KafkaBrokers, 1, messaging.WithDLT(topics...)...); err != nil {
		return nil, err
	}
	handlers := kafka.New(svc, rt.Log)
	// Отдельный consumer на топик: ядовитое сообщение или долгий повтор в одном топике не держат остальные.
	for topic, handle := range map[string]messaging.Handler{
		contracts.TopicTransferCommands: handlers.Commands,
		contracts.TopicInventoryEvents:  handlers.Inventory,
		contracts.TopicLogisticsReplies: handlers.Logistics,
	} {
		c, err := messaging.NewConsumer(messaging.ConsumerConfig{
			Brokers: rt.Cfg.KafkaBrokers, Group: consumerGroup + "." + topic, Topics: []string{topic},
		}, handle, rt.Log)
		if err != nil {
			return nil, err
		}
		workers = append(workers, c.Run)
	}
	return workers, nil
}
