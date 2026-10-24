package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/adapters/postgres/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

func (Store) RaiseEscalation(ctx context.Context, db app.DB, n app.EscalationRecord) (bool, error) {
	tag, err := db.Exec(ctx, `INSERT INTO escalation(escalation_id,obligation_id,reason,raised_at) VALUES($1,$2,$3,$4) ON CONFLICT(obligation_id,reason) DO NOTHING`, n.ID, n.ObligationID, n.Reason, n.RaisedAt)
	return tag.RowsAffected() > 0, err
}
func (Store) Escalations(ctx context.Context, db app.DB, includeClosed bool, limit int, id *uuid.UUID) ([]app.EscalationRecord, error) {
	rows, err := db.Query(ctx, `SELECT escalation_id,obligation_id,reason,raised_at,closed_at,coalesce(closed_by,''),coalesce(resolution,''),coalesce(comment,'') FROM escalation WHERE ($1::boolean OR closed_at IS NULL) AND ($3::uuid IS NULL OR obligation_id=$3) ORDER BY (closed_at IS NOT NULL),raised_at DESC,escalation_id LIMIT $2`, includeClosed, limit, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []app.EscalationRecord{}
	for rows.Next() {
		var n app.EscalationRecord
		if err := rows.Scan(&n.ID, &n.ObligationID, &n.Reason, &n.RaisedAt, &n.ClosedAt, &n.ClosedBy, &n.Resolution, &n.Comment); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
func (Store) CloseEscalation(ctx context.Context, db app.DB, id uuid.UUID, user, resolution, comment string, at time.Time) (app.EscalationRecord, bool, error) {
	var n app.EscalationRecord
	err := db.QueryRow(ctx, `UPDATE escalation SET closed_at=$2,closed_by=$3,resolution=$4,comment=$5 WHERE escalation_id=$1 AND closed_at IS NULL RETURNING escalation_id,obligation_id,reason,raised_at,closed_at,closed_by,resolution,comment`, id, at, user, resolution, comment).Scan(&n.ID, &n.ObligationID, &n.Reason, &n.RaisedAt, &n.ClosedAt, &n.ClosedBy, &n.Resolution, &n.Comment)
	if errors.Is(err, pgx.ErrNoRows) {
		return n, false, nil
	}
	return n, err == nil, err
}
func (Store) Transport(ctx context.Context, db app.DB, id uuid.UUID) (app.TransportRecord, error) {
	var raw []byte
	var n app.TransportRecord
	if err := db.QueryRow(ctx, `SELECT payload FROM obligation_transport WHERE obligation_id=$1`, id).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return n, app.ErrNotFound
		}
		return n, err
	}
	err := json.Unmarshal(raw, &n)
	return n, err
}
func (Store) SaveTransport(ctx context.Context, db app.DB, n app.TransportRecord) error {
	raw, err := json.Marshal(n)
	if err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `INSERT INTO obligation_transport VALUES($1,$2) ON CONFLICT(obligation_id) DO UPDATE SET payload=excluded.payload`, n.ObligationID, raw); err != nil {
		return err
	}
	violations := n.Violations
	if violations == nil {
		violations = []string{}
	}
	_, err = db.Exec(ctx, `UPDATE obligation SET clinical_use_blocked=clinical_use_blocked OR $2,transport_violations=CASE WHEN $2::boolean THEN $3::text[] ELSE transport_violations END WHERE obligation_id=$1`, n.ObligationID, n.ConditionsViolated, violations)
	return err
}
func (Store) DispatchUnits(ctx context.Context, db app.DB, id uuid.UUID, units []uuid.UUID, at time.Time) (int, error) {
	tag, err := db.Exec(ctx, `UPDATE unit_copy SET local_dispatch_obligation_id=$1 WHERE unit_id=ANY($2::uuid[]) AND local_dispatch_obligation_id IS NULL AND NOT clinical_use_blocked AND unit_status_code='IN_STOCK' AND expires_at>$3`, id, units, at)
	if err != nil {
		return 0, err
	}
	if int(tag.RowsAffected()) != len(units) {
		return 0, app.ErrConflict
	}
	tag, err = db.Exec(ctx, `UPDATE allocation SET status='CONSUMED',closed_at=$2 WHERE obligation_id=$1 AND status='CONFIRMED'`, id, at)
	if err != nil {
		return 0, err
	}
	if int(tag.RowsAffected()) != len(units) {
		return 0, app.ErrConflict
	}
	return int(tag.RowsAffected()), nil
}
func (Store) BlockTransportUnits(ctx context.Context, db app.DB, id uuid.UUID) error {
	_, err := db.Exec(ctx, `UPDATE unit_copy SET clinical_use_blocked=true WHERE unit_id IN(SELECT unit_id FROM allocation WHERE obligation_id=$1 AND status='CONSUMED')`, id)
	return err
}
func (Store) UnitsForView(ctx context.Context, db app.DB, id uuid.UUID) ([]uuid.UUID, error) {
	rows, err := db.Query(ctx, `SELECT unit_id FROM allocation WHERE obligation_id=$1 AND status IN('CONFIRMED','CONSUMED') ORDER BY created_at,allocation_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}
func (Store) AllObligations(ctx context.Context, db app.DB, status *domain.Status, limit, offset int) ([]domain.Obligation, error) {
	var st *string
	if status != nil {
		v := string(*status)
		st = &v
	}
	rows, err := db.Query(ctx, `SELECT * FROM obligation WHERE ($1::text IS NULL OR status=$1) ORDER BY created_at DESC,obligation_id LIMIT $2 OFFSET $3`, st, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records, err := pgx.CollectRows(rows, pgx.RowToStructByPos[gen.Obligation])
	if err != nil {
		return nil, err
	}
	out := make([]domain.Obligation, 0, len(records))
	for _, r := range records {
		o, err := toObligation(r)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}
