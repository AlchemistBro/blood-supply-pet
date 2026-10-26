package observability

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestDLTCounterHasProvisionedAlertsPrometheusName(t *testing.T) {
	registry := prometheus.NewRegistry()
	exporter, err := otelprom.New(otelprom.WithRegisterer(registry))
	if err != nil {
		t.Fatal(err)
	}
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	hooks, err := ConsumerMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatal(err)
	}
	hooks.DeadLetter("logistics.commands", "poison")
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == "messaging_consumer_dead_letters_total" {
			if len(family.Metric) != 1 || family.Metric[0].GetCounter().GetValue() != 1 {
				t.Fatalf("unexpected exported DLT counter: %+v", family)
			}
			return
		}
	}
	t.Fatal("provisioned Grafana DLT alert cannot find the exported metric name")
}

func TestConsumerCallbacksExportActualEvents(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	hooks, err := ConsumerMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatal(err)
	}
	var before metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &before); err != nil {
		t.Fatal(err)
	}
	for _, scope := range before.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name == "messaging.consumer.dead_letters" {
				t.Fatal("DLT metric fabricated before any acknowledged DLT write")
			}
		}
	}
	hooks.Handled("logistics.commands", "RequestTransport", time.Second, nil)
	hooks.Handled("logistics.commands", "RequestTransport", 2*time.Second, errors.New("retry"))
	hooks.DeadLetter("logistics.commands", "poison")
	var after metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &after); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, scope := range after.ScopeMetrics {
		for _, m := range scope.Metrics {
			seen[m.Name] = true
			if m.Name == "messaging.consumer.dead_letters" {
				sum := m.Data.(metricdata.Sum[int64])
				if !sum.IsMonotonic || len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 1 {
					t.Fatalf("wrong DLT sum: %+v", sum)
				}
				attrs := sum.DataPoints[0].Attributes.ToSlice()
				if len(attrs) != 2 || attrs[0].Key != "topic" || attrs[0].Value.AsString() != "logistics.commands" || attrs[1].Key != "type" || attrs[1].Value.AsString() != "poison" {
					t.Fatalf("wrong DLT labels: %+v", attrs)
				}
			}
			if m.Name == "messaging.consumer.handled" {
				sum := m.Data.(metricdata.Sum[int64])
				if len(sum.DataPoints) != 2 || sum.DataPoints[0].Value != 1 || sum.DataPoints[1].Value != 1 {
					t.Fatalf("success/failure attempts not counted separately: %+v", sum)
				}
			}
		}
	}
	for _, name := range []string{"messaging.consumer.dead_letters", "messaging.consumer.handled", "messaging.consumer.duration"} {
		if !seen[name] {
			t.Fatalf("missing metric %s", name)
		}
	}
}
