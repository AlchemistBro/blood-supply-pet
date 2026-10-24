package postgres

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/services/planning/internal/app"
)

func (s *Store) RebuildProjections(ctx context.Context, tx pgx.Tx) (int64, error) {
	if _, err := tx.Exec(ctx, `DELETE FROM planning_input_projection`); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO planning_input_projection(input_key,source_version,occurred_at,payload) SELECT DISTINCT ON(input_key) input_key,source_version,occurred_at,payload FROM planning_projection_journal ORDER BY input_key,seq DESC`)
	return tag.RowsAffected(), err
}
func (s *Store) PlanObligations(ctx context.Context, tx pgx.Tx, id uuid.UUID) ([]app.ObligationProgress, error) {
	rows, err := tx.Query(ctx, `SELECT payload FROM planning_input_projection WHERE input_key LIKE 'obligation:%' AND payload->>'request_id'=$1 ORDER BY input_key`, id.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]app.ObligationProgress, 0)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var n struct {
			ID      uuid.UUID   `json:"obligation_id"`
			Source  int64       `json:"source_clinic_id"`
			Target  int64       `json:"target_clinic_id"`
			Kind    string      `json:"component_kind"`
			Status  string      `json:"status"`
			Units   []uuid.UUID `json:"units"`
			Blocked bool        `json:"conditions_violated"`
		}
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, err
		}
		out = append(out, app.ObligationProgress{ID: n.ID, Source: n.Source, Target: n.Target, Kind: n.Kind, Status: n.Status, Units: len(n.Units), Blocked: n.Blocked})
	}
	return out, rows.Err()
}
