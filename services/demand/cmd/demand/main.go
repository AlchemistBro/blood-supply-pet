// demand — сервис территориальной потребности (ФП2): приём CSV аналитика, история расхода, доли район → клиника.
package main

import (
	"context"

	"github.com/AlchemistBro/blood-supply-pet/libs/runtime"
	apispec "github.com/AlchemistBro/blood-supply-pet/services/demand/api"
	"github.com/AlchemistBro/blood-supply-pet/services/demand/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/demand/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/services/demand/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/demand/migrations"
)

func main() {
	runtime.Run(runtime.Spec{
		Name:       "demand",
		Module:     "demand",
		Migrations: migrations.FS,
		OpenAPI:    apispec.Spec,
		Setup: func(_ context.Context, rt *runtime.Runtime) ([]runtime.Worker, error) {
			ref, err := app.ReferenceFrom(rt.Refdata)
			if err != nil {
				return nil, err
			}
			svc := app.New(rt.Pool, postgres.New(rt.Pool), rt.Journal, rt.Clock, ref, rt.Log)
			api.Mount(rt.Mux, svc, rt.Deny())
			return nil, nil
		},
	})
}
