package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/app"
)

// ProgressStore фиксирует факт и его публикацию одной транзакцией; HTTP выполняется до неё.
type ProgressStore struct {
	Pool    *pgxpool.Pool
	Journal *messaging.Journal
}

var progressCourierPattern = regexp.MustCompile(logistics.CourierRefPattern)

func (s ProgressStore) Status(ctx context.Context) ([]app.TransportStatus, error) {
	rows, err := s.Pool.Query(ctx, `SELECT obligation_id,status,coalesce(shipment_ref,''),coalesce(last_error,''),updated_at,dispatch_reported,delivery_reported FROM transport_request ORDER BY updated_at DESC,obligation_id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByPos[app.TransportStatus])
}

func (s ProgressStore) ProgressDue(ctx context.Context, now time.Time, limit int) ([]app.ProgressOrder, error) {
	rows, err := s.Pool.Query(ctx, `UPDATE transport_request SET progress_checked_at=$1 WHERE obligation_id IN
 (SELECT obligation_id FROM transport_request WHERE status='ACCEPTED' AND NOT cancel_requested AND NOT delivery_reported AND (progress_checked_at IS NULL OR progress_checked_at <= $1 - interval '5 seconds') ORDER BY progress_checked_at NULLS FIRST,obligation_id LIMIT $2 FOR UPDATE SKIP LOCKED) RETURNING obligation_id,shipment_ref`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByPos[app.ProgressOrder])
}
func (s ProgressStore) SaveProgress(ctx context.Context, row app.ProgressOrder, p app.ShipmentProgress) error {
	if p.DispatchedAt == nil {
		return nil
	}
	if !progressCourierPattern.MatchString(p.CourierRef) {
		return errors.New("C03: отсутствует служебный код работника")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if p.DeliveredAt != nil && p.DeliveredAt.Before(*p.DispatchedAt) {
		return errors.New("C03: прибытие раньше отправки")
	}
	return bspgx.WithTx(ctx, s.Pool, func(ctx context.Context, tx pgx.Tx) error {
		var dispatched, delivered bool
		err := tx.QueryRow(ctx, `SELECT dispatch_reported,delivery_reported FROM transport_request WHERE obligation_id=$1 AND status='ACCEPTED' AND NOT cancel_requested AND shipment_ref=$2 FOR UPDATE`, row.ID, row.ShipmentRef).Scan(&dispatched, &delivered)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !dispatched {
			e := logistics.TransportDispatched{ObligationID: row.ID, ShipmentRef: row.ShipmentRef, DispatchedAt: *p.DispatchedAt, CourierRef: p.CourierRef, TemperatureAtDispatchC: p.DepartureTemperature, ContainerIntact: p.DepartureContainerIntact}
			if _, err := s.Journal.Publish(ctx, tx, contracts.TopicLogisticsReplies, row.ID.String(), e); err != nil {
				return err
			}
		}
		if p.DeliveredAt != nil && !delivered {
			minutes := int(p.DeliveredAt.Sub(*p.DispatchedAt) / time.Minute)
			e := logistics.TransportDelivered{ObligationID: row.ID, ShipmentRef: row.ShipmentRef, DeliveredAt: *p.DeliveredAt, DepartedAt: p.DispatchedAt, DurationMin: &minutes, TemperatureAtDispatchC: p.DepartureTemperature, TemperatureAtArrivalC: p.ArrivalTemperature, ContainerIntact: p.ArrivalContainerIntact, TempExcursion: p.TemperatureExcursion, CourierRef: p.CourierRef}
			if _, err := s.Journal.Publish(ctx, tx, contracts.TopicLogisticsReplies, row.ID.String(), e); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE transport_request SET dispatch_reported=true,delivery_reported=delivery_reported OR $2,progress_payload=$3 WHERE obligation_id=$1`, row.ID, p.DeliveredAt != nil, raw)
		return err
	})
}
