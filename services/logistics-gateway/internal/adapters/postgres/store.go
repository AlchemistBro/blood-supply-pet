// Package postgres — хранилище заказов перевозки на PostgreSQL (запросы sqlc в gen). Реализует app.Store.
//
// Аренда строки (lease_owner, lease_until) — то, на чём держится «один заказ» при нескольких экземплярах шлюза:
// её берёт один, только он может записать исход, а у упавшего она истекает сама.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/adapters/postgres/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/domain"
)

// Store — заказы перевозки.
type Store struct {
	q *gen.Queries
}

// New создаёт хранилище над пулом; методы с tx работают в транзакции вызывающего.
func New(pool *pgxpool.Pool) *Store {
	return &Store{q: gen.New(pool)}
}

// Insert записывает строку REQUESTING. false — строка уже есть: повтор команды или надгробие отмены.
func (s *Store) Insert(ctx context.Context, tx pgx.Tx, r domain.Request, now time.Time) (bool, error) {
	quantity, err := toInt32(r.Quantity)
	if err != nil {
		return false, err
	}
	n, err := s.q.WithTx(tx).InsertRequest(ctx, gen.InsertRequestParams{
		ObligationID:      r.ObligationID,
		RequestID:         &r.RequestID,
		SourceClinicID:    &r.SourceClinicID,
		TargetClinicID:    &r.TargetClinicID,
		ComponentKindCode: &r.ComponentKindCode,
		Quantity:          &quantity,
		TemperatureRegime: &r.TemperatureRegime,
		Now:               now,
	})
	if err != nil {
		return false, fmt.Errorf("вставка transport_request: %w", err)
	}
	return n == 1, nil
}

// RequestCancel: строки нет — надгробие CANCELLED; есть — флаг отмены и немедленная очередь сверщику.
// Команды по одному обязательству идут в одной партиции по порядку, поэтому гонки двух вставок надгробия нет;
// если она всё же случится, вторая упрётся в первичный ключ и consumer повторит команду, уже увидев строку.
func (s *Store) RequestCancel(ctx context.Context, tx pgx.Tx, id uuid.UUID, now time.Time) (domain.Status, bool, error) {
	q := s.q.WithTx(tx)
	before, err := q.LockStatus(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := q.InsertTombstone(ctx, gen.InsertTombstoneParams{ObligationID: id, Now: now}); err != nil {
			return "", false, fmt.Errorf("надгробие отмены: %w", err)
		}
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("блокировка transport_request: %w", err)
	}
	if err := q.FlagCancel(ctx, gen.FlagCancelParams{ObligationID: id, Now: now}); err != nil {
		return "", false, fmt.Errorf("флаг отмены: %w", err)
	}
	return domain.Status(before), true, nil
}

// Claim берёт аренду вне транзакции вызывающего: UPDATE ... RETURNING сам по себе атомарен.
// now и until — по часам экземпляра; требуется синхронизация времени экземпляров (см. app.Settings).
func (s *Store) Claim(ctx context.Context, id, owner uuid.UUID, now, until time.Time) (domain.Row, bool, error) {
	r, err := s.q.Claim(ctx, gen.ClaimParams{ObligationID: id, Owner: owner, LeaseUntil: until, Now: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Row{}, false, nil
	}
	if err != nil {
		return domain.Row{}, false, fmt.Errorf("аренда transport_request: %w", err)
	}
	return toRow(r), true, nil
}

// Finish записывает исход под арендой owner при статусе expected. Пустой shipmentRef не стирает записанный номер.
// cancelRequested — флаг отмены на момент записи.
func (s *Store) Finish(ctx context.Context, tx pgx.Tx, id, owner uuid.UUID, expected, status domain.Status,
	shipmentRef string, now time.Time) (finished, cancelRequested bool, err error) {
	cancelRequested, err = s.q.WithTx(tx).Finish(ctx, gen.FinishParams{
		ObligationID: id, Owner: owner, ExpectedStatus: string(expected), Status: string(status),
		ShipmentRef: optional(shipmentRef), Now: now,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("исход transport_request: %w", err)
	}
	return true, cancelRequested, nil
}

// FailAttempt засчитывает неудачную попытку под арендой owner при статусе expected.
func (s *Store) FailAttempt(ctx context.Context, tx pgx.Tx, id, owner uuid.UUID, expected, status domain.Status,
	lastError string, nextAttemptAt, now time.Time) (bool, error) {
	n, err := s.q.WithTx(tx).FailAttempt(ctx, gen.FailAttemptParams{
		ObligationID: id, Owner: owner, ExpectedStatus: string(expected), Status: string(status),
		LastError: optional(lastError), NextAttemptAt: nextAttemptAt, Now: now,
	})
	if err != nil {
		return false, fmt.Errorf("неудачная попытка transport_request: %w", err)
	}
	return n == 1, nil
}

// Release отпускает аренду owner без исхода и без засчитанной попытки.
func (s *Store) Release(ctx context.Context, id, owner uuid.UUID, now time.Time) error {
	if _, err := s.q.Release(ctx, gen.ReleaseParams{ObligationID: id, Owner: owner, Now: now}); err != nil {
		return fmt.Errorf("освобождение аренды transport_request: %w", err)
	}
	return nil
}

// Due — очередь сверщика.
func (s *Store) Due(ctx context.Context, now time.Time, limit int) ([]uuid.UUID, error) {
	l, err := toInt32(limit)
	if err != nil {
		return nil, err
	}
	ids, err := s.q.Due(ctx, gen.DueParams{Now: now, BatchLimit: l})
	if err != nil {
		return nil, fmt.Errorf("очередь сверщика: %w", err)
	}
	return ids, nil
}

// Find читает строку.
func (s *Store) Find(ctx context.Context, id uuid.UUID) (domain.Row, bool, error) {
	r, err := s.q.Find(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Row{}, false, nil
	}
	if err != nil {
		return domain.Row{}, false, fmt.Errorf("чтение transport_request: %w", err)
	}
	return toRow(r), true, nil
}

func toRow(r gen.TransportRequest) domain.Row {
	row := domain.Row{
		Request: domain.Request{
			ObligationID:      r.ObligationID,
			SourceClinicID:    deref(r.SourceClinicID),
			TargetClinicID:    deref(r.TargetClinicID),
			ComponentKindCode: deref(r.ComponentKindCode),
			Quantity:          int(deref(r.Quantity)),
			TemperatureRegime: deref(r.TemperatureRegime),
		},
		Status:          domain.Status(r.Status),
		ShipmentRef:     deref(r.ShipmentRef),
		CancelRequested: r.CancelRequested,
		Attempts:        int(r.Attempts),
		LastError:       deref(r.LastError),
	}
	if r.RequestID != nil {
		row.RequestID = *r.RequestID
	}
	return row
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func toInt32(v int) (int32, error) {
	if v < 0 || v > 1<<31-1 {
		return 0, fmt.Errorf("значение %d вне диапазона integer", v)
	}
	return int32(v), nil
}
