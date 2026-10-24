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

const normLock int64 = 0x6e6f726d

func (s *Store) CurrentNorm(ctx context.Context, tx pgx.Tx, clinicID int64, kind string) (*app.NormRecord, error) {
	var n app.NormRecord
	err := tx.QueryRow(ctx, `SELECT n.stock_norm_id,n.clinic_id,n.component_kind_code,a.consumption_year,a.annual_volume_ml,n.avg_monthly_ml::float8,n.norm_ml::float8,n.is_min_unit_applied,n.version,n.calculated_at FROM stock_norm n JOIN annual_consumption a USING(annual_consumption_id) WHERE n.clinic_id=$1 AND n.component_kind_code=$2 AND n.is_current`, clinicID, kind).Scan(&n.ID, &n.ClinicID, &n.Kind, &n.Year, &n.Annual, &n.MonthlyML, &n.NormML, &n.Minimum, &n.Version, &n.At)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &n, err
}

func (s *Store) NextNormVersion(ctx context.Context, tx pgx.Tx) (int64, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, normLock); err != nil {
		return 0, err
	}
	var version int64
	err := tx.QueryRow(ctx, `SELECT nextval('stock_norm_version_seq')`).Scan(&version)
	return version, err
}

func (s *Store) SaveNorm(ctx context.Context, tx pgx.Tx, n app.NormRecord, annualID uuid.UUID, actor string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO stock_norm_state(clinic_id,component_kind_code) VALUES($1,$2) ON CONFLICT DO NOTHING`, n.ClinicID, n.Kind); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM stock_norm_state WHERE clinic_id=$1 AND component_kind_code=$2 FOR UPDATE`, n.ClinicID, n.Kind); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO annual_consumption VALUES($1,$2,$3,$4,$5,$6,$7)`, annualID, n.ClinicID, n.Kind, n.Year, n.Annual, actor, n.At); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE stock_norm SET is_current=false WHERE clinic_id=$1 AND component_kind_code=$2 AND is_current`, n.ClinicID, n.Kind); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO stock_norm(stock_norm_id,annual_consumption_id,clinic_id,component_kind_code,avg_monthly_ml,norm_ml,is_min_unit_applied,is_current,calculated_at,version) VALUES($1,$2,$3,$4,$5,$6,$7,true,$8,$9)`, n.ID, annualID, n.ClinicID, n.Kind, n.MonthlyML, n.NormML, n.Minimum, n.At, n.Version)
	return err
}

func (s *Store) Norms(ctx context.Context, clinicID int64) ([]app.NormRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT n.stock_norm_id,n.clinic_id,n.component_kind_code,a.consumption_year,a.annual_volume_ml,n.avg_monthly_ml::float8,n.norm_ml::float8,n.is_min_unit_applied,n.version,n.calculated_at FROM stock_norm n JOIN annual_consumption a USING(annual_consumption_id) WHERE n.clinic_id=$1 AND n.is_current ORDER BY n.component_kind_code`, clinicID)
	if err != nil {
		return nil, fmt.Errorf("нормативы: %w", err)
	}
	defer rows.Close()
	out := make([]app.NormRecord, 0)
	for rows.Next() {
		var n app.NormRecord
		if err := rows.Scan(&n.ID, &n.ClinicID, &n.Kind, &n.Year, &n.Annual, &n.MonthlyML, &n.NormML, &n.Minimum, &n.Version, &n.At); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) CheckNormThresholds(ctx context.Context, tx pgx.Tx, at time.Time) ([]app.NormThreshold, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, normLock); err != nil {
		return nil, err
	}
	// Все строки блокируются в одном порядке; повтор ниже порога не создаёт повторных событий (FR014).
	rows, err := tx.Query(ctx, `SELECT st.clinic_id,st.component_kind_code,st.below,n.norm_ml::float8,COALESCE((SELECT sum(a.volume_ml)::bigint FROM actual_stock_at($1) a WHERE a.clinic_id=st.clinic_id AND a.component_kind_code=st.component_kind_code),0) FROM stock_norm_state st JOIN stock_norm n ON n.clinic_id=st.clinic_id AND n.component_kind_code=st.component_kind_code AND n.is_current ORDER BY st.clinic_id,st.component_kind_code FOR UPDATE OF st`, at)
	if err != nil {
		return nil, err
	}
	type state struct {
		app.NormThreshold
		old bool
	}
	var states []state
	for rows.Next() {
		var st state
		if err := rows.Scan(&st.ClinicID, &st.Kind, &st.old, &st.NormML, &st.StockML); err != nil {
			rows.Close()
			return nil, err
		}
		states = append(states, st)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var out []app.NormThreshold
	for _, st := range states {
		below := float64(st.StockML) < st.NormML
		if below == st.old {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE stock_norm_state SET below=$3 WHERE clinic_id=$1 AND component_kind_code=$2`, st.ClinicID, st.Kind, below); err != nil {
			return nil, err
		}
		if below {
			out = append(out, st.NormThreshold)
		}
	}
	return out, nil
}
