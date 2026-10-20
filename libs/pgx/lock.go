package pgx

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Lease — удерживаемая сессионная advisory-блокировка. Пока соединение живо, ключ занят.
type Lease struct {
	conn *pgxpool.Conn
	key  int64
}

// TryAdvisoryLock пытается взять сессионную advisory-блокировку по ключу на выделенном соединении.
// Возвращает nil, nil, если ключ занят другим экземпляром. Используется, чтобы ретранслятор outbox
// работал ровно в одном экземпляре сервиса (ADR-0003: порядок сообщений).
func TryAdvisoryLock(ctx context.Context, pool *pgxpool.Pool, key int64) (*Lease, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("соединение для advisory lock: %w", err)
	}
	var ok bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&ok); err != nil {
		conn.Release()
		return nil, fmt.Errorf("pg_try_advisory_lock: %w", err)
	}
	if !ok {
		conn.Release()
		return nil, nil
	}
	return &Lease{conn: conn, key: key}, nil
}

// Release снимает блокировку и возвращает соединение в пул.
func (l *Lease) Release(ctx context.Context) {
	if l == nil || l.conn == nil {
		return
	}
	_, _ = l.conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", l.key)
	l.conn.Release()
	l.conn = nil
}

// Alive проверяет, что соединение, держащее блокировку, ещё живо.
func (l *Lease) Alive(ctx context.Context) bool {
	if l == nil || l.conn == nil {
		return false
	}
	return l.conn.Ping(ctx) == nil
}
