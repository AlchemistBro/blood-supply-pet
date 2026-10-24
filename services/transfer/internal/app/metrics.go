package app

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

// metrics — счётчики сценариев transfer (выводятся в /metrics через libs/observability).
type metrics struct {
	expiryFailures metric.Int64Counter
}

func newMetrics() metrics {
	m := otel.GetMeterProvider().Meter("github.com/AlchemistBro/blood-supply-pet/services/transfer")
	c, err := m.Int64Counter("transfer.expiry.failures",
		metric.WithDescription("Обязательства, которые таймер срока не смог перевести в EXPIRED (повтор на следующем проходе)"))
	if err != nil {
		c = noop.Int64Counter{}
	}
	return metrics{expiryFailures: c}
}

func (m metrics) expiryFailed(ctx context.Context) { m.expiryFailures.Add(ctx, 1) }
