// Package postgres — хранилище копии запаса inventory на PostgreSQL (запросы sqlc в gen).
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/adapters/postgres/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
)

// Store реализует app.Store.
type Store struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

var _ app.Store = (*Store)(nil)

// New создаёт хранилище.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: gen.New(pool)}
}

// LockUnitByCode блокирует строку единицы по коду АИСТ-М.
func (s *Store) LockUnitByCode(ctx context.Context, tx pgx.Tx, code string) (*domain.Unit, error) {
	row, err := s.q.WithTx(tx).GetUnitByCodeForUpdate(ctx, code)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("блокировка единицы: %w", err)
	}
	u := toDomain(row)
	return &u, nil
}

// InsertUnit добавляет единицу; false — код уже занят.
func (s *Store) InsertUnit(ctx context.Context, tx pgx.Tx, u domain.Unit) (bool, error) {
	n, err := s.q.WithTx(tx).InsertUnit(ctx, gen.InsertUnitParams{
		UnitID: u.UnitID, AistUnitCode: u.AistUnitCode, ClinicID: u.ClinicID, StorageLocation: u.StorageLocation,
		ComponentKindCode: u.ComponentKind, VolumeMl: u.VolumeML, BloodGroupCode: u.BloodGroup,
		Phenotype: nullable(u.Phenotype), ExpiresAt: u.ExpiresAt, UnitStatusCode: u.Status,
		StatusReceivedAt: u.StatusReceivedAt, SourceVersion: u.Version,
	})
	if err != nil {
		return false, fmt.Errorf("вставка единицы: %w", err)
	}
	return n == 1, nil
}

// UpdateUnitIfNewer обновляет единицу, только если версия новее сохранённой.
func (s *Store) UpdateUnitIfNewer(ctx context.Context, tx pgx.Tx, u domain.Unit) (bool, error) {
	n, err := s.q.WithTx(tx).UpdateUnitIfNewer(ctx, gen.UpdateUnitIfNewerParams{
		ClinicID: u.ClinicID, StorageLocation: u.StorageLocation, ComponentKindCode: u.ComponentKind,
		VolumeMl: u.VolumeML, BloodGroupCode: u.BloodGroup, Phenotype: nullable(u.Phenotype),
		ExpiresAt: u.ExpiresAt, UnitStatusCode: u.Status, StatusReceivedAt: u.StatusReceivedAt,
		SourceVersion: u.Version, UnitID: u.UnitID,
	})
	if err != nil {
		return false, fmt.Errorf("обновление единицы: %w", err)
	}
	return n == 1, nil
}

// InsertStatusHistory пишет строку истории статусов; повтор версии игнорируется.
func (s *Store) InsertStatusHistory(ctx context.Context, tx pgx.Tx, id uuid.UUID, u domain.Unit, receivedAt time.Time) error {
	err := s.q.WithTx(tx).InsertStatusHistory(ctx, gen.InsertStatusHistoryParams{
		UnitStatusHistoryID: id, UnitID: u.UnitID, UnitStatusCode: u.Status, SourceVersion: u.Version,
		ChangedAt: u.ChangedAt, ReceivedAt: receivedAt,
	})
	if err != nil {
		return fmt.Errorf("история статусов: %w", err)
	}
	return nil
}

// UnitsByIDs возвращает единицы копии по unit_id; неизвестных в ответе нет.
func (s *Store) UnitsByIDs(ctx context.Context, ids []uuid.UUID) ([]domain.Unit, error) {
	rows, err := s.q.ListUnitsByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("чтение единиц: %w", err)
	}
	out := make([]domain.Unit, 0, len(rows))
	for _, r := range rows {
		out = append(out, toDomain(r))
	}
	return out, nil
}

// StartSyncRun открывает сеанс обмена.
func (s *Store) StartSyncRun(ctx context.Context, run app.SyncRun) error {
	_, err := s.q.StartSyncRun(ctx, gen.StartSyncRunParams{
		SyncRunID: run.ID, SyncKind: run.Kind, RequestedBy: run.RequestedBy, StartedAt: run.StartedAt,
		SourceVersion: run.SourceVersion, ClinicID: run.ClinicID,
	})
	if err != nil {
		return fmt.Errorf("начало сеанса обмена: %w", err)
	}
	return nil
}

// AdvanceSyncRun двигает курсор сеанса в транзакции страницы. tx = nil — вне транзакции.
func (s *Store) AdvanceSyncRun(ctx context.Context, tx pgx.Tx, id uuid.UUID, sourceVersion int64, changes int) error {
	q := s.q
	if tx != nil {
		q = q.WithTx(tx)
	}
	err := q.AdvanceSyncRun(ctx, gen.AdvanceSyncRunParams{SourceVersion: sourceVersion, Changes: int64(changes), SyncRunID: id})
	if err != nil {
		return fmt.Errorf("курсор сеанса обмена: %w", err)
	}
	return nil
}

// FinishSyncRun закрывает сеанс с результатом и примечанием (ошибка или пересинхронизация).
func (s *Store) FinishSyncRun(ctx context.Context, id uuid.UUID, finishedAt time.Time, result string, note *string) error {
	_, err := s.q.FinishSyncRun(ctx, gen.FinishSyncRunParams{FinishedAt: &finishedAt, SyncResult: &result, ErrorText: note, SyncRunID: id})
	if err != nil {
		return fmt.Errorf("окончание сеанса обмена: %w", err)
	}
	return nil
}

// LastSyncCursor — курсор последнего сеанса CHANGES.
func (s *Store) LastSyncCursor(ctx context.Context) (int64, error) {
	v, err := s.q.LastSyncCursor(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("курсор обмена: %w", err)
	}
	return v, nil
}

// Watermark — окончание последнего успешного сеанса CHANGES.
func (s *Store) Watermark(ctx context.Context) (*time.Time, error) {
	t, err := s.q.LastSuccessfulSync(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("водяной знак: %w", err)
	}
	return t, nil
}

// NextProtectedVolumeVersion выдаёт следующую версию защищённого объёма.
func (s *Store) NextProtectedVolumeVersion(ctx context.Context, tx pgx.Tx) (int64, error) {
	v, err := s.q.WithTx(tx).NextProtectedVolumeVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("версия защищённого объёма: %w", err)
	}
	return v, nil
}

func toDomain(r gen.Unit) domain.Unit {
	u := domain.Unit{UnitID: r.UnitID, StatusReceivedAt: r.StatusReceivedAt.UTC()}
	u.AistUnitCode = r.AistUnitCode
	u.ClinicID = r.ClinicID
	u.ComponentKind = r.ComponentKindCode
	u.BloodGroup = r.BloodGroupCode
	if r.Phenotype != nil {
		u.Phenotype = *r.Phenotype
	}
	u.VolumeML = r.VolumeMl
	u.ExpiresAt = r.ExpiresAt.UTC()
	u.StorageLocation = r.StorageLocation
	u.Status = r.UnitStatusCode
	u.Version = r.SourceVersion
	// Время изменения в АИСТ-М в копии не хранится: ближайшее известное — время получения статуса.
	u.ChangedAt = r.StatusReceivedAt.UTC()
	return u
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
