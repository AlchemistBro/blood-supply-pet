package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/adapters/postgres/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
)

// Запросы к функциям фактического запаса (миграция 00111) написаны вручную: sqlc их не разбирает.
// Момент годности передаётся параметром — время часов сервиса, а не now() базы.

const actualStockSQL = `
SELECT component_kind_code, blood_group_code, unit_count, volume_ml, nearest_expiry
FROM actual_stock_at($1)
WHERE clinic_id = $2
ORDER BY component_kind_code, blood_group_code`

// Порядок FEFO совпадает с domain.SortFEFO: годность, затем unit_id.
const availableUnitsSQL = `
SELECT unit_id, aist_unit_code, clinic_id, storage_location, component_kind_code, volume_ml,
       blood_group_code, phenotype, expires_at, unit_status_code, status_received_at, source_version
FROM available_unit_at($1)
WHERE clinic_id = $2
  AND ($3::varchar IS NULL OR component_kind_code = $3::varchar)
  AND ($4::varchar IS NULL OR blood_group_code = $4::varchar)
ORDER BY %s
LIMIT $5 OFFSET $6`

// ActualStock — фактический запас клиники на момент at.
func (s *Store) ActualStock(ctx context.Context, clinicID int64, at time.Time) ([]app.StockLine, error) {
	rows, err := s.pool.Query(ctx, actualStockSQL, at, clinicID)
	if err != nil {
		return nil, fmt.Errorf("фактический запас: %w", err)
	}
	lines, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (app.StockLine, error) {
		var l app.StockLine
		err := r.Scan(&l.ComponentKind, &l.BloodGroup, &l.UnitCount, &l.VolumeML, &l.NearestExpiry)
		l.NearestExpiry = l.NearestExpiry.UTC()
		return l, err
	})
	if err != nil {
		return nil, fmt.Errorf("фактический запас: %w", err)
	}
	return lines, nil
}

// AvailableUnits — единицы фактического запаса клиники.
func (s *Store) AvailableUnits(ctx context.Context, f app.UnitFilter) ([]domain.Unit, error) {
	order := "unit_id"
	if f.FEFO {
		order = "expires_at, unit_id"
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(availableUnitsSQL, order),
		f.At, f.ClinicID, nullable(f.ComponentKind), nullable(f.BloodGroup), f.Limit, f.Offset)
	if err != nil {
		return nil, fmt.Errorf("единицы запаса: %w", err)
	}
	units, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Unit, error) {
		var g gen.Unit
		err := r.Scan(&g.UnitID, &g.AistUnitCode, &g.ClinicID, &g.StorageLocation, &g.ComponentKindCode, &g.VolumeMl,
			&g.BloodGroupCode, &g.Phenotype, &g.ExpiresAt, &g.UnitStatusCode, &g.StatusReceivedAt, &g.SourceVersion)
		return toDomain(g), err
	})
	if err != nil {
		return nil, fmt.Errorf("единицы запаса: %w", err)
	}
	return units, nil
}

// GetUnit — единица копии; nil, если её нет.
func (s *Store) GetUnit(ctx context.Context, id uuid.UUID) (*domain.Unit, error) {
	row, err := s.q.GetUnit(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("единица: %w", err)
	}
	u := toDomain(row)
	return &u, nil
}

// UnitHistory — история статусов единицы.
func (s *Store) UnitHistory(ctx context.Context, id uuid.UUID) ([]app.StatusChange, error) {
	rows, err := s.q.ListStatusHistory(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("история статусов: %w", err)
	}
	out := make([]app.StatusChange, 0, len(rows))
	for _, r := range rows {
		out = append(out, app.StatusChange{
			Status: r.UnitStatusCode, SourceVersion: r.SourceVersion, ChangedAt: r.ChangedAt.UTC(), ReceivedAt: r.ReceivedAt.UTC(),
		})
	}
	return out, nil
}

// LastSyncRun — последний сеанс вида kind (или последний успешный).
func (s *Store) LastSyncRun(ctx context.Context, kind string, successOnly bool) (*app.SyncRunView, error) {
	var row gen.SyncRun
	var err error
	if successOnly {
		row, err = s.q.LastSuccessfulSyncRunOfKind(ctx, kind)
	} else {
		row, err = s.q.LastSyncRunOfKind(ctx, kind)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("последний сеанс обмена: %w", err)
	}
	v := runView(row)
	return &v, nil
}

// ListSyncRuns — журнал сеансов обмена.
func (s *Store) ListSyncRuns(ctx context.Context, f app.SyncRunFilter) ([]app.SyncRunView, error) {
	rows, err := s.q.ListSyncRuns(ctx, gen.ListSyncRunsParams{SyncKind: nullable(f.Kind), BeforeID: f.BeforeID, RowLimit: int64(f.Limit)})
	if err != nil {
		return nil, fmt.Errorf("журнал сеансов обмена: %w", err)
	}
	out := make([]app.SyncRunView, 0, len(rows))
	for _, r := range rows {
		out = append(out, runView(r))
	}
	return out, nil
}

func runView(r gen.SyncRun) app.SyncRunView {
	v := app.SyncRunView{
		ID: r.SyncRunID, Kind: r.SyncKind, RequestedBy: r.RequestedBy, StartedAt: r.StartedAt.UTC(),
		Result: r.SyncResult, ChangesCount: int(r.ChangesCount), Note: r.ErrorText, SourceVersion: r.SourceVersion,
	}
	if r.FinishedAt != nil {
		t := r.FinishedAt.UTC()
		v.FinishedAt = &t
	}
	return v
}
