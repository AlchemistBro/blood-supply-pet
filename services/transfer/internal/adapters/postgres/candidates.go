package postgres

import (
	"context"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

func (Store) Candidates(ctx context.Context, db app.DB, o domain.Obligation, at time.Time, limit int) ([]domain.UnitCopy, error) {
	rows, err := db.Query(ctx, `SELECT u.unit_id,u.clinic_id,u.component_kind_code,u.blood_group_code,u.rh_positive,u.phenotype,u.volume_ml,u.expires_at FROM unit_copy u WHERE u.clinic_id=$1 AND u.component_kind_code=$2 AND u.unit_status_code='IN_STOCK' AND u.expires_at>$3 AND NOT EXISTS(SELECT 1 FROM allocation a WHERE a.unit_id=u.unit_id AND a.status='CONFIRMED') ORDER BY u.expires_at,u.unit_id`, o.SourceClinicID, o.ComponentKind, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.UnitCopy{}
	for rows.Next() {
		var u domain.UnitCopy
		if err := rows.Scan(&u.UnitID, &u.ClinicID, &u.ComponentKind, &u.BloodGroup, &u.RhPositive, &u.Phenotype, &u.VolumeML, &u.ExpiresAt); err != nil {
			return nil, err
		}
		u.Status = domain.UnitInStock
		if o.Spec.Mismatch(o.SourceClinicID, o.ComponentKind, u) != "" {
			continue
		}
		out = append(out, u)
		if len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}
