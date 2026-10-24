package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/services/planning/internal/app"
)

func (s *Store) RecordDeficit(ctx context.Context, tx pgx.Tx, n app.DeficitRecord) error {
	_, err := tx.Exec(ctx, `INSERT INTO deficit_record VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, n.ID, n.Type, n.OriginID, n.ClinicID, n.Kind, n.Group, n.ML, n.Units, n.Reason, n.At)
	return err
}
func (s *Store) Deficits(ctx context.Context, clinic int64, kind, reason string) ([]app.DeficitRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT deficit_record_id,record_type,origin_id,clinic_id,component_kind,blood_group,uncovered_ml,uncovered_units,reason,created_at FROM deficit_record WHERE ($1::bigint=0 OR clinic_id=$1) AND ($2='' OR record_type=$2) AND ($3='' OR reason=$3) ORDER BY created_at DESC,deficit_record_id DESC LIMIT 200`, clinic, kind, reason)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []app.DeficitRecord{}
	for rows.Next() {
		var n app.DeficitRecord
		if err := rows.Scan(&n.ID, &n.Type, &n.OriginID, &n.ClinicID, &n.Kind, &n.Group, &n.ML, &n.Units, &n.Reason, &n.At); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
