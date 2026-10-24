package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/adapters/postgres/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
)

func (s *Store) CorrectUnitFromSnapshot(ctx context.Context, tx pgx.Tx, u domain.Unit) error {
	_, err := s.q.WithTx(tx).CorrectUnitFromSnapshot(ctx, gen.CorrectUnitFromSnapshotParams{ClinicID: u.ClinicID, StorageLocation: u.StorageLocation, ComponentKindCode: u.ComponentKind, VolumeMl: u.VolumeML, BloodGroupCode: u.BloodGroup, Phenotype: nullable(u.Phenotype), ExpiresAt: u.ExpiresAt, UnitStatusCode: u.Status, StatusReceivedAt: u.StatusReceivedAt, SourceVersion: u.Version, UnitID: u.UnitID})
	return err
}

func (s *Store) SnapshotMissingUnits(ctx context.Context, tx pgx.Tx, codes []string) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `SELECT unit_id FROM unit WHERE NOT (aist_unit_code=ANY($1::text[])) ORDER BY unit_id`, codes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) CompleteDataSync(ctx context.Context, tx pgx.Tx, runID uuid.UUID, at time.Time, result string, discrepancies int, note *string, withSnapshot bool) ([]inventory.StockSnapshotPublished, error) {
	var version int64
	if err := tx.QueryRow(ctx, `UPDATE sync_run SET finished_at=$2,sync_result=$3,discrepancies_count=$4,error_text=$5 WHERE sync_run_id=$1 RETURNING sync_seq`, runID, at, result, discrepancies, note).Scan(&version); err != nil {
		return nil, err
	}
	if result != "SUCCESS" || !withSnapshot {
		return nil, nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO stock_snapshot_key SELECT DISTINCT clinic_id,component_kind_code,blood_group_code FROM unit ON CONFLICT DO NOTHING`); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT k.clinic_id,k.component_kind_code,k.blood_group_code,COALESCE(a.unit_count,0),COALESCE(a.volume_ml,0),a.nearest_expiry FROM stock_snapshot_key k LEFT JOIN actual_stock_at($1) a USING(clinic_id,component_kind_code,blood_group_code) ORDER BY k.clinic_id,k.component_kind_code,k.blood_group_code`, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []inventory.StockSnapshotPublished
	for rows.Next() {
		n := inventory.StockSnapshotPublished{Watermark: version, SyncedAt: at}
		if err := rows.Scan(&n.ClinicID, &n.ComponentKind, &n.BloodGroup, &n.UnitCount, &n.VolumeML, &n.NearestExpiry); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
