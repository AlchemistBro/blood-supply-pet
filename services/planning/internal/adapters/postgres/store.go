package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/int02"
	"github.com/AlchemistBro/blood-supply-pet/services/planning/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/planning/internal/domain"
)

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

var _ app.Store = (*Store)(nil)

func (s *Store) Project(ctx context.Context, tx pgx.Tx, p app.Projection) error {
	_, err := tx.Exec(ctx, `INSERT INTO planning_input_projection VALUES($1,$2,$3,$4) ON CONFLICT(input_key) DO UPDATE SET source_version=EXCLUDED.source_version,occurred_at=EXCLUDED.occurred_at,payload=planning_input_projection.payload||EXCLUDED.payload WHERE EXCLUDED.source_version>planning_input_projection.source_version OR (EXCLUDED.source_version=planning_input_projection.source_version AND EXCLUDED.occurred_at>planning_input_projection.occurred_at)`, p.Key, p.Version, p.At, p.Payload)
	return err
}
func (s *Store) Projection(ctx context.Context, tx pgx.Tx, key string) (json.RawMessage, error) {
	var raw json.RawMessage
	err := tx.QueryRow(ctx, `SELECT payload FROM planning_input_projection WHERE input_key=$1 FOR UPDATE`, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, app.ErrNotFound
	}
	return raw, err
}
func (s *Store) Inputs(ctx context.Context, tx pgx.Tx) ([]app.Projection, error) {
	rows, err := tx.Query(ctx, `SELECT input_key,source_version,occurred_at,payload FROM planning_input_projection ORDER BY input_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []app.Projection
	for rows.Next() {
		var p app.Projection
		if err := rows.Scan(&p.Key, &p.Version, &p.At, &p.Payload); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) InsertForecast(ctx context.Context, tx pgx.Tx, id uuid.UUID, f int02.EmergencyForecast, at time.Time) (bool, error) {
	raw, err := json.Marshal(f)
	if err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO emergency_forecast VALUES($1,$2,$3,$4) ON CONFLICT(scenario_version) DO NOTHING`, id, f.ScenarioVersion, raw, at)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	var same bool
	err = tx.QueryRow(ctx, `SELECT payload=$2::jsonb FROM emergency_forecast WHERE scenario_version=$1`, f.ScenarioVersion, raw).Scan(&same)
	if err != nil {
		return false, err
	}
	if !same {
		return false, app.ErrConflict
	}
	return false, nil
}
func (s *Store) Forecast(ctx context.Context, tx pgx.Tx, id uuid.UUID) (int02.EmergencyForecast, error) {
	var raw []byte
	var err error
	if tx != nil {
		err = tx.QueryRow(ctx, `SELECT payload FROM emergency_forecast WHERE emergency_forecast_id=$1`, id).Scan(&raw)
	} else {
		err = s.pool.QueryRow(ctx, `SELECT payload FROM emergency_forecast WHERE emergency_forecast_id=$1`, id).Scan(&raw)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return int02.EmergencyForecast{}, app.ErrNotFound
	}
	if err != nil {
		return int02.EmergencyForecast{}, err
	}
	var f int02.EmergencyForecast
	err = json.Unmarshal(raw, &f)
	return f, err
}

func (s *Store) Forecasts(ctx context.Context) ([]app.ForecastRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT emergency_forecast_id,received_at,payload FROM emergency_forecast ORDER BY received_at DESC,emergency_forecast_id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []app.ForecastRecord{}
	for rows.Next() {
		var n app.ForecastRecord
		var raw []byte
		if err := rows.Scan(&n.ID, &n.ReceivedAt, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &n.Forecast); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
func (s *Store) LockBuild(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1886151022)`)
	return err
}
func (s *Store) NextVersion(ctx context.Context, tx pgx.Tx) (int64, error) {
	var v int64
	err := tx.QueryRow(ctx, `SELECT nextval('replenishment_plan_version_seq')`).Scan(&v)
	return v, err
}
func (s *Store) SavePlan(ctx context.Context, tx pgx.Tx, in domain.Input, n app.PlanRecord) error {
	snapshot, err := json.Marshal(in)
	if err != nil {
		return err
	}
	result, err := json.Marshal(n.Plan)
	if err != nil {
		return err
	}
	var forecast *uuid.UUID
	if in.Forecast != nil {
		forecast = &in.Forecast.ForecastID
	}
	_, err = tx.Exec(ctx, `INSERT INTO replenishment_plan(replenishment_plan_id,demand_estimate_id,emergency_forecast_id,stock_watermark,stock_synced_at,coverage_ratio,elapsed_ms,created_at,input_snapshot,result) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, n.Plan.PlanID, in.Versions.DemandEstimateID, forecast, in.Versions.StockWatermark, in.Versions.StockSyncedAt, n.Plan.CoverageRatio, n.ElapsedMS, n.Plan.CreatedAt, snapshot, result)
	return err
}

func (s *Store) SupersedePlans(ctx context.Context, tx pgx.Tx, id uuid.UUID) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `UPDATE replenishment_plan SET superseded=true WHERE replenishment_plan_id<>$1 AND NOT superseded RETURNING replenishment_plan_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}
func (s *Store) ReadPlan(ctx context.Context, tx pgx.Tx, id uuid.UUID) (app.PlanRecord, error) {
	var raw []byte
	var n app.PlanRecord
	var err error
	if tx != nil {
		err = tx.QueryRow(ctx, `SELECT result,elapsed_ms FROM replenishment_plan WHERE replenishment_plan_id=$1`, id).Scan(&raw, &n.ElapsedMS)
	} else {
		err = s.pool.QueryRow(ctx, `SELECT result,elapsed_ms FROM replenishment_plan WHERE replenishment_plan_id=$1`, id).Scan(&raw, &n.ElapsedMS)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return n, app.ErrNotFound
	}
	if err != nil {
		return n, err
	}
	err = json.Unmarshal(raw, &n.Plan)
	return n, err
}
func (s *Store) Plans(ctx context.Context) ([]app.PlanRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT result,elapsed_ms FROM replenishment_plan ORDER BY created_at DESC,replenishment_plan_id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]app.PlanRecord, 0)
	for rows.Next() {
		var n app.PlanRecord
		var raw []byte
		if err := rows.Scan(&raw, &n.ElapsedMS); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &n.Plan); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
