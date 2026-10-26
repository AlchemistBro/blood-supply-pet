package observability

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
)

// ConsumerMetrics connects the real consumer callbacks to the runtime's OTel
// provider (both /metrics and OTLP). DeadLetter is called only after Kafka has
// acknowledged the DLT write. No message IDs or payloads are metric labels.
func ConsumerMetrics(meter metric.Meter) (messaging.ConsumerMetrics, error) {
	handled, err := meter.Int64Counter("messaging.consumer.handled")
	if err != nil {
		return messaging.ConsumerMetrics{}, err
	}
	duration, err := meter.Float64Histogram("messaging.consumer.duration", metric.WithUnit("s"))
	if err != nil {
		return messaging.ConsumerMetrics{}, err
	}
	dead, err := meter.Int64Counter("messaging.consumer.dead_letters")
	if err != nil {
		return messaging.ConsumerMetrics{}, err
	}
	return messaging.ConsumerMetrics{
		Handled: func(topic, typ string, d time.Duration, handleErr error) {
			outcome := "ok"
			if handleErr != nil {
				outcome = "error"
			}
			attrs := metric.WithAttributes(attribute.String("topic", topic), attribute.String("type", typ), attribute.String("outcome", outcome))
			handled.Add(context.Background(), 1, attrs)
			duration.Record(context.Background(), d.Seconds(), attrs)
		},
		DeadLetter: func(topic, typ string) {
			dead.Add(context.Background(), 1, metric.WithAttributes(attribute.String("topic", topic), attribute.String("type", typ)))
		},
	}, nil
}
