// transfer - сервис АС распределения компонентов донорской крови.
package main

import (
	"context"

	"github.com/AlchemistBro/blood-supply-pet/libs/runtime"
	apispec "github.com/AlchemistBro/blood-supply-pet/services/transfer/api"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/migrations"
)

func main() {
	runtime.Run(runtime.Spec{
		Name:       "transfer",
		Module:     "transfer",
		Migrations: migrations.FS,
		OpenAPI:    apispec.Spec,
		Setup: func(_ context.Context, rt *runtime.Runtime) ([]runtime.Worker, error) {
			// Здесь: собрать адаптеры, приложение, зарегистрировать маршруты на rt.Mux,
			// вернуть фоновых воркеров (consumer, таймеры).
			_ = rt
			return nil, nil
		},
	})
}
