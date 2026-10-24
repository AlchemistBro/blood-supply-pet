package postgres

import (
	"context"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
)

func (Store) UpdateUnitCopy(ctx context.Context, db app.DB, e app.UnitUpdated, now time.Time) (bool, error) {
	tag, err := db.Exec(ctx, `UPDATE unit_copy SET clinic_id=$2,component_kind_code=$3,unit_status_code=$4,expires_at=coalesce($5,expires_at),status_reason=NULL,version=$6,updated_at=$7,local_dispatch_obligation_id=CASE WHEN clinic_id<>$2 THEN NULL ELSE local_dispatch_obligation_id END WHERE unit_id=$1 AND version<$6`, e.UnitID, e.ClinicID, e.ComponentKind, string(e.Status), e.ExpiresAt, e.Version, now)
	return tag.RowsAffected() == 1, err
}
