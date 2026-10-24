package app

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// observeSecurity читает сохранённые срабатывания: откат транзакции не увеличивает счётчик.
func (s *Service) observeSecurity() error {
	meter := otel.Meter("blood-supply/audit")
	incidents, err := meter.Int64ObservableCounter("security_incidents_suspected_total")
	if err != nil {
		return err
	}
	chain, err := meter.Int64ObservableGauge("audit_chain_verify_ok")
	if err != nil {
		return err
	}
	last, err := meter.Int64ObservableGauge("audit_chain_verify_last_run_timestamp_seconds")
	if err != nil {
		return err
	}
	_, err = meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		rows, err := s.pool.Query(ctx, `SELECT rule,count(*) FROM security_incident GROUP BY rule`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rule string
			var count int64
			if err := rows.Scan(&rule, &count); err != nil {
				return err
			}
			o.ObserveInt64(incidents, count, metric.WithAttributes(attribute.String("rule", rule)))
		}
		o.ObserveInt64(chain, s.verifyOK.Load())
		o.ObserveInt64(last, s.verifyLast.Load())
		return rows.Err()
	}, incidents, chain, last)
	return err
}
