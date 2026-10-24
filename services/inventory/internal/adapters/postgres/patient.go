package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/app"
)

var _ app.PatientQueue = (*Store)(nil)

func (s *Store) EnqueuePatient(ctx context.Context, tx pgx.Tx, j app.PatientJob, at time.Time) error {
	tag, err := tx.Exec(ctx, `INSERT INTO aist_outbox_message(idempotency_key,obligation_id,message_type,payload,created_at,next_attempt_at) VALUES($1,$2,$3,$4,$5,$5) ON CONFLICT DO NOTHING`, j.Key, j.ObligationID, j.Type, j.Payload, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var same bool
	if err := tx.QueryRow(ctx, `SELECT obligation_id=$2 AND message_type=$3 AND payload=$4::jsonb FROM aist_outbox_message WHERE idempotency_key=$1`, j.Key, j.ObligationID, j.Type, j.Payload).Scan(&same); err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("%w: ключ ИНТ-05 повторён с другим содержимым", app.ErrInvalid)
	}
	return nil
}
func (s *Store) ClaimPatient(ctx context.Context, owner uuid.UUID, at, until time.Time) (app.PatientJob, bool, error) {
	var j app.PatientJob
	err := s.pool.QueryRow(ctx, `WITH candidate AS (SELECT j.id FROM aist_outbox_message j WHERE j.acknowledged_at IS NULL AND j.next_attempt_at<=$1 AND (j.lease_until IS NULL OR j.lease_until<=$1) AND NOT EXISTS(SELECT 1 FROM aist_outbox_message prev WHERE prev.obligation_id=j.obligation_id AND prev.id<j.id AND prev.acknowledged_at IS NULL) ORDER BY j.id FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE aist_outbox_message j SET lease_owner=$2,lease_until=$3,attempts=attempts+1 FROM candidate WHERE j.id=candidate.id RETURNING j.id,j.idempotency_key,j.obligation_id,j.message_type,j.payload,j.attempts`, at, owner, until).Scan(&j.ID, &j.Key, &j.ObligationID, &j.Type, &j.Payload, &j.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return j, false, nil
	}
	return j, err == nil, err
}
func (s *Store) FinishPatient(ctx context.Context, tx pgx.Tx, j app.PatientJob, owner uuid.UUID, at time.Time, compensated bool) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE aist_outbox_message SET acknowledged_at=$3,released_at=CASE WHEN $4::boolean THEN $3 ELSE released_at END,lease_owner=NULL,lease_until=NULL WHERE id=$1 AND lease_owner=$2 AND acknowledged_at IS NULL`, j.ID, owner, at, compensated)
	return tag.RowsAffected() > 0, err
}

func (s *Store) PatientCompensationKey(ctx context.Context, j app.PatientJob) (uuid.UUID, bool, error) {
	var key uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT idempotency_key FROM aist_outbox_message WHERE obligation_id=$1 AND id>$2 AND message_type='ReleasePatientAllocation' ORDER BY id LIMIT 1`, j.ObligationID, j.ID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	return key, err == nil, err
}
func (s *Store) FailPatient(ctx context.Context, j app.PatientJob, owner uuid.UUID, next time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE aist_outbox_message SET next_attempt_at=$3,lease_owner=NULL,lease_until=NULL WHERE id=$1 AND lease_owner=$2 AND acknowledged_at IS NULL`, j.ID, owner, next)
	return err
}
func (s *Store) PatientUnitCodes(ctx context.Context, ids []uuid.UUID) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT aist_unit_code FROM unit WHERE unit_id=ANY($1::uuid[]) ORDER BY aist_unit_code`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	codes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	if len(codes) != len(ids) {
		return nil, fmt.Errorf("единицы ИНТ-05 ещё не синхронизированы")
	}
	return codes, nil
}
func (s *Store) ActivePatientKeys(ctx context.Context, j app.PatientJob) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT idempotency_key FROM aist_outbox_message WHERE obligation_id=$1 AND id<$2 AND message_type='AllocateUnitsToPatient' AND acknowledged_at IS NOT NULL AND released_at IS NULL ORDER BY id`, j.ObligationID, j.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}
func (s *Store) MarkPatientReleased(ctx context.Context, tx pgx.Tx, j app.PatientJob, at time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE aist_outbox_message SET released_at=$3 WHERE obligation_id=$1 AND id<$2 AND message_type='AllocateUnitsToPatient' AND acknowledged_at IS NOT NULL AND released_at IS NULL`, j.ObligationID, j.ID, at)
	return err
}
