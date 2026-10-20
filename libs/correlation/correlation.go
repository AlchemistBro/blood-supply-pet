// Package correlation переносит сквозные идентификаторы запроса через контекст:
// из HTTP-заголовка в outbox, из Kafka-заголовков в обработчик и дальше в логи и трассы.
// Отдельный пакет, чтобы httpx и messaging не зависели друг от друга.
package correlation

import (
	"context"

	"github.com/google/uuid"
)

// IDs — корреляция (весь сценарий пользователя) и причина (сообщение, вызвавшее текущее действие).
type IDs struct {
	Correlation string
	Causation   string
}

type ctxKey struct{}

// With кладёт идентификаторы в контекст.
func With(ctx context.Context, ids IDs) context.Context {
	return context.WithValue(ctx, ctxKey{}, ids)
}

// From достаёт идентификаторы; пустые, если их нет.
func From(ctx context.Context) IDs {
	ids, _ := ctx.Value(ctxKey{}).(IDs)
	return ids
}

// Ensure возвращает контекст с корреляцией: существующей или новой.
func Ensure(ctx context.Context) (context.Context, IDs) {
	ids := From(ctx)
	if ids.Correlation == "" {
		ids.Correlation = NewID()
		ctx = With(ctx, ids)
	}
	return ctx, ids
}

// NewID создаёт uuid v7 в строковом виде (упорядочен по времени).
func NewID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}
