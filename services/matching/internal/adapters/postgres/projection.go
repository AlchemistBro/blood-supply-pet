package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/app"
)

func (s *Store) Project(ctx context.Context, tx pgx.Tx, p app.Projection) (bool, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO matching_input_projection VALUES($1,$2,$3,$4) ON CONFLICT(input_key) DO UPDATE SET source_version=excluded.source_version,occurred_at=excluded.occurred_at,payload=matching_input_projection.payload||excluded.payload WHERE excluded.source_version>matching_input_projection.source_version OR (excluded.source_version=matching_input_projection.source_version AND excluded.occurred_at>matching_input_projection.occurred_at)`, p.Key, p.Version, p.At, p.Payload)
	return tag.RowsAffected() > 0, err
}
func (s *Store) Projection(ctx context.Context, tx pgx.Tx, key string) (app.Projection, error) {
	var p app.Projection
	err := tx.QueryRow(ctx, `SELECT input_key,source_version,occurred_at,payload FROM matching_input_projection WHERE input_key=$1 FOR UPDATE`, key).Scan(&p.Key, &p.Version, &p.At, &p.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, app.ErrNotFound
	}
	return p, err
}
func (s *Store) Inputs(ctx context.Context, tx pgx.Tx) ([]app.Projection, error) {
	rows, err := tx.Query(ctx, `SELECT input_key,source_version,occurred_at,payload FROM matching_input_projection ORDER BY input_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []app.Projection{}
	for rows.Next() {
		var p app.Projection
		if err := rows.Scan(&p.Key, &p.Version, &p.At, &p.Payload); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) RequestInputs(ctx context.Context, tx pgx.Tx, id uuid.UUID) ([]app.Projection, error) {
	query := `SELECT input_key,source_version,occurred_at,payload FROM matching_input_projection WHERE (input_key LIKE 'obligation:%' AND payload->>'request_id'=$1) OR (input_key LIKE 'allocation:%' AND payload->>'obligation_id' IN (SELECT payload->>'obligation_id' FROM matching_input_projection WHERE input_key LIKE 'obligation:%' AND payload->>'request_id'=$1)) ORDER BY input_key`
	var rows pgx.Rows
	var err error
	if tx != nil {
		rows, err = tx.Query(ctx, query, id.String())
	} else {
		rows, err = s.pool.Query(ctx, query, id.String())
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []app.Projection{}
	for rows.Next() {
		var p app.Projection
		if err := rows.Scan(&p.Key, &p.Version, &p.At, &p.Payload); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
