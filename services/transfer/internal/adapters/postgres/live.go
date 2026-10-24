package postgres

import (
	"context"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
)

func (Store) LiveAvailable(ctx context.Context, db app.DB, clinic int64, kind string, at time.Time) (int, error) {
	var count int
	err := db.QueryRow(ctx, `SELECT count(*) FROM unit_copy WHERE clinic_id=$1 AND component_kind_code=$2 AND unit_status_code='IN_STOCK' AND expires_at>$3`, clinic, kind, at).Scan(&count)
	return count, err
}
