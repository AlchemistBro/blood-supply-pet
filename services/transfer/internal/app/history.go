package app

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// HistoryEntry — безопасная для экрана часть локального аудита без details и номера случая.
type HistoryEntry struct {
	ID     int64     `json:"id"`
	At     time.Time `json:"occurred_at"`
	Action string    `json:"action"`
	Result string    `json:"result"`
}

func (s *Service) History(ctx context.Context, id uuid.UUID, limit, offset int) ([]HistoryEntry, error) {
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, fmt.Errorf("%w: неверная страница истории", ErrInvalidEvent)
	}
	return s.store.History(ctx, s.pool, id, limit, offset)
}
