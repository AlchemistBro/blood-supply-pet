package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
)

func (Store) LockRequest(ctx context.Context, db app.DB, id uuid.UUID) error {
	_, err := db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,1886151024))`, id.String())
	return err
}
func (Store) RequestCancelled(ctx context.Context, db app.DB, id uuid.UUID) (bool, error) {
	var cancelled bool
	err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM cancelled_request WHERE request_id=$1)`, id).Scan(&cancelled)
	return cancelled, err
}
func (Store) CancelRequest(ctx context.Context, db app.DB, id uuid.UUID, reason string, at time.Time) error {
	_, err := db.Exec(ctx, `INSERT INTO cancelled_request VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, reason, at)
	return err
}
