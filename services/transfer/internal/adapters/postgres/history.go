package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
)

func (s *Store) History(ctx context.Context, db app.DB, id uuid.UUID, limit, offset int) ([]app.HistoryEntry, error) {
	rows, err := db.Query(ctx, `SELECT id,occurred_at,action,result FROM audit_local WHERE object_type='obligation' AND object_id=$1 ORDER BY id DESC LIMIT $2 OFFSET $3`, id.String(), limit, offset)
	if err != nil {
		return nil, fmt.Errorf("история обязательства: %w", err)
	}
	defer rows.Close()
	items := make([]app.HistoryEntry, 0)
	for rows.Next() {
		var item app.HistoryEntry
		if err := rows.Scan(&item.ID, &item.At, &item.Action, &item.Result); err != nil {
			return nil, fmt.Errorf("строка истории: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
