// logistics-gateway - сервис АС распределения компонентов донорской крови.
package main

import (
	"context"

	"github.com/AlchemistBro/blood-supply-pet/libs/runtime"
	apispec "github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/api"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/migrations"
)

func main() {
	runtime.Run(runtime.Spec{
		Name:       "logistics-gateway",
		Module:     "logistics",
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
