package messaging

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// FirstTime отмечает сообщение принятым в транзакции обработчика. Возвращает false, если это повтор:
// Kafka доставляет «хотя бы один раз», а первичный ключ inbox делает обработку идемпотентной.
func FirstTime(ctx context.Context, tx pgx.Tx, msg Message, now time.Time) (bool, error) {
	tag, err := tx.Exec(ctx,
		`INSERT INTO inbox (message_id, message_type, received_at) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		msg.ID, msg.Type, now)
	if err != nil {
		return false, fmt.Errorf("запись в inbox: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
