// inventory — копия единиц компонентов крови из АИСТ-М, события о них и точечный статус единиц для transfer.
package main

import (
	"context"

	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/runtime"
	apispec "github.com/AlchemistBro/blood-supply-pet/services/inventory/api"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/adapters/aistclient"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/adapters/events"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/config"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/migrations"
)

func main() {
	runtime.Run(runtime.Spec{
		Name:       "inventory",
		Module:     auditc.ModuleInventory,
		Migrations: migrations.FS,
		OpenAPI:    apispec.Spec,
		Setup: func(_ context.Context, rt *runtime.Runtime) ([]runtime.Worker, error) {
			cfg, err := config.Load()
			if err != nil {
				return nil, err
			}
			svc := app.New(app.Deps{
				Pool: rt.Pool, Store: postgres.New(rt.Pool),
				Aist:      aistclient.New(cfg.AistURL, aistclient.Options{StatusTimeout: cfg.StatusTimeout}),
				Publisher: events.New(rt.Journal), Journal: rt.Journal, Ref: rt.Refdata, Clock: rt.Clock, Log: rt.Log,
				Config: app.Config{
					PollInterval: cfg.PollInterval, PageLimit: cfg.PageLimit, StatusTimeout: cfg.StatusTimeout,
					StaleAfter: cfg.StaleAfter,
				},
			})
			api.Mount(rt.Mux, svc, rt.Deny(), api.Options{DevEnabled: rt.Cfg.Environment == "local"})
			return []runtime.Worker{svc.RunSync, svc.RunSnapshots}, nil
		},
	})
}
