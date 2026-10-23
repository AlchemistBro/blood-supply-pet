package app

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
)

// AistUnit — единица в формате АИСТ-М (ИНТ-01, GET /units/changes).
type AistUnit struct {
	AistUnitCode     string
	ClinicCode       string
	ComponentKind    string
	BloodGroup       string
	Phenotype        string
	VolumeML         int64
	ExpiresAt        time.Time
	StorageLocation  string
	Status           string
	WithdrawalReason string
	Version          int64
	ChangedAt        time.Time
}

// ChangesPage — страница изменений. LastVersion — курсор для следующего запроса; CurrentVersion — последняя
// версия источника: если она меньше курсора, АИСТ-М потеряла состояние (перезапуск имитатора).
type ChangesPage struct {
	Units          []AistUnit
	LastVersion    int64
	CurrentVersion int64
	HasMore        bool
}

// AistStatus — точечный статус единицы (ИНТ-01, GET /units/status).
type AistStatus struct {
	AistUnitCode     string
	Status           string
	WithdrawalReason string
	Version          int64
	ChangedAt        time.Time
}

// StatusPage — ответ АИСТ-М на точечный запрос статусов.
type StatusPage struct {
	Units     []AistStatus
	NotFound  []string
	CheckedAt time.Time
}

// AistClient — порт АИСТ-М (C01). Реализация ходит по HTTP с таймаутом и предохранителем.
type AistClient interface {
	Changes(ctx context.Context, since int64, limit int) (ChangesPage, error)
	// Statuses спрашивает статусы по кодам АИСТ-М; длинный список делится на пачки по ограничению источника.
	Statuses(ctx context.Context, codes []string) (StatusPage, error)
}

// SyncRun — сеанс обмена с АИСТ-М.
type SyncRun struct {
	ID            uuid.UUID
	Kind          string
	RequestedBy   string
	StartedAt     time.Time
	SourceVersion int64
}

// Store — порт хранилища копии запаса. Методы с tx работают в транзакции бизнес-операции.
type Store interface {
	// LockUnitByCode блокирует строку единицы по коду АИСТ-М; nil — единицы ещё нет.
	LockUnitByCode(ctx context.Context, tx pgx.Tx, code string) (*domain.Unit, error)
	// InsertUnit добавляет единицу; false — код уже занят параллельной вставкой.
	InsertUnit(ctx context.Context, tx pgx.Tx, u domain.Unit) (bool, error)
	// UpdateUnitIfNewer обновляет единицу, только если версия новее сохранённой.
	UpdateUnitIfNewer(ctx context.Context, tx pgx.Tx, u domain.Unit) (bool, error)
	InsertStatusHistory(ctx context.Context, tx pgx.Tx, id uuid.UUID, u domain.Unit, receivedAt time.Time) error
	UnitsByIDs(ctx context.Context, ids []uuid.UUID) ([]domain.Unit, error)

	StartSyncRun(ctx context.Context, run SyncRun) error
	AdvanceSyncRun(ctx context.Context, tx pgx.Tx, id uuid.UUID, sourceVersion int64, changes int) error
	FinishSyncRun(ctx context.Context, id uuid.UUID, finishedAt time.Time, result string, note *string) error
	// LastSyncCursor — курсор последнего сеанса CHANGES; 0, если сеансов не было.
	LastSyncCursor(ctx context.Context) (int64, error)
	// Watermark — окончание последнего успешного сеанса CHANGES; nil, если его не было.
	Watermark(ctx context.Context) (*time.Time, error)

	NextProtectedVolumeVersion(ctx context.Context, tx pgx.Tx) (int64, error)
}

// Publisher — порт событий inventory.events: пишет в outbox в транзакции изменения (ADR-0003).
type Publisher interface {
	// PublishUnit публикует событие по применённому изменению единицы (ключ — unit_id).
	PublishUnit(ctx context.Context, tx pgx.Tx, ch domain.Change) error
	// PublishProtectedVolume публикует защищённый объём (ключ — clinic_id:kind).
	PublishProtectedVolume(ctx context.Context, tx pgx.Tx, pv domain.ProtectedVolume) error
}
