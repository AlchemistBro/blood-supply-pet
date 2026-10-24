package app

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

// UnitStatusChecker — порт единственного синхронного вызова между сервисами: transfer → inventory,
// точечный статус единиц в АИСТ-М (ADR-0004). Вызывается до транзакции и вне блокировок, таймаут 3 с в адаптере.
type UnitStatusChecker interface {
	CheckUnits(ctx context.Context, ids []uuid.UUID) (UnitStatusReport, error)
}

// UnitStatusReport — ответ inventory. Единицы, которых нет в ответе, считаются недоступными.
type UnitStatusReport struct {
	Statuses map[uuid.UUID]domain.UnitStatus
	// FromCopy — АИСТ-М не ответила, inventory отдал статус из своей копии (source: copy).
	FromCopy bool
}

// ErrSourceSystemUnavailable — inventory или АИСТ-М не ответили (таймаут, предохранитель, 5xx).
var ErrSourceSystemUnavailable = errors.New("АИСТ-М недоступна")

// ErrNotFound — обязательство не найдено.
var ErrNotFound = errors.New("обязательство не найдено")

// ErrUnitTaken — нарушен UNIQUE (unit_id) WHERE status = 'CONFIRMED' (NFR004): единицу закрепил другой.
// Транзакция после этого прервана и откатывается целиком.
var ErrUnitTaken = errors.New("единица уже закреплена")

// ErrInvalidEvent — входящее событие не разбирается; повтор его не исправит (в DLT).
var ErrInvalidEvent = errors.New("некорректное событие")

// DB — пул или транзакция. Чтение вне транзакции идёт через пул, изменения только через tx.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store — порт хранилища transfer. Каждый переход статуса — CAS по ожидаемому статусу и версии из o.
type Store interface {
	SetPatientDelivery(context.Context, DB, uuid.UUID, uuid.UUID, string) error
	AcknowledgePatient(context.Context, DB, uuid.UUID, uuid.UUID, time.Time) error
	Candidates(context.Context, DB, domain.Obligation, time.Time, int) ([]domain.UnitCopy, error)
	LiveAvailable(context.Context, DB, int64, string, time.Time) (int, error)
	LockRequest(context.Context, DB, uuid.UUID) error
	RequestCancelled(context.Context, DB, uuid.UUID) (bool, error)
	CancelRequest(context.Context, DB, uuid.UUID, string, time.Time) error
	History(ctx context.Context, db DB, id uuid.UUID, limit, offset int) ([]HistoryEntry, error)
	InsertObligation(ctx context.Context, db DB, o domain.Obligation) (bool, error)
	Obligation(ctx context.Context, db DB, id uuid.UUID) (domain.Obligation, error)
	LockObligation(ctx context.Context, db DB, id uuid.UUID) (domain.Obligation, error)
	ObligationIDByChoice(ctx context.Context, db DB, choiceID uuid.UUID) (uuid.UUID, error)
	ObligationsByRequest(ctx context.Context, db DB, requestID uuid.UUID) ([]domain.Obligation, error)
	// ObligationsBySource — задания клиники-источника, новые первыми; status nil — любой.
	ObligationsBySource(ctx context.Context, db DB, sourceClinicID int64, status *domain.Status, limit int) ([]domain.Obligation, error)
	PendingDueBy(ctx context.Context, db DB, now time.Time, limit int) ([]uuid.UUID, error)
	UrgencyWindow(ctx context.Context, db DB, urgencyCode string) (time.Duration, error)
	// UnknownClinics — клиники из списка, которых нет в справочнике ref_clinic.
	UnknownClinics(ctx context.Context, db DB, ids []int64) ([]int64, error)

	CASStatus(ctx context.Context, db DB, o domain.Obligation, next domain.Status, reason string, now time.Time) (bool, error)
	CASConfirm(ctx context.Context, db DB, o domain.Obligation, qty int, key string, now time.Time) (bool, error)
	CASTouch(ctx context.Context, db DB, o domain.Obligation, now time.Time) (bool, error)
	CASTransportRequested(ctx context.Context, db DB, id uuid.UUID, shipmentRef string, now time.Time) (bool, error)
	// MarkEscalated отмечает эскалацию PENDING-задания; false — уже было или задание не PENDING.
	MarkEscalated(ctx context.Context, db DB, id uuid.UUID, now time.Time) (bool, error)

	// LockStock блокирует строку Остатка (создаёт пустую, если её нет). Всегда первая блокировка.
	LockStock(ctx context.Context, db DB, clinicID int64, kind string) (domain.Stock, error)
	AddReserved(ctx context.Context, db DB, clinicID int64, kind string, delta int) error
	RecountAvailable(ctx context.Context, db DB, clinicID int64, kind string) error
	SetProtectedVolume(ctx context.Context, db DB, clinicID int64, kind string, volume int, version int64, reason string) (bool, error)

	UpsertUnitCopy(ctx context.Context, db DB, u domain.UnitCopy, now time.Time) (bool, error)
	SetUnitCopyStatus(ctx context.Context, db DB, unitID uuid.UUID, s domain.UnitStatus, reason string, version int64, now time.Time) (bool, error)
	UnitCopies(ctx context.Context, db DB, ids []uuid.UUID) (map[uuid.UUID]domain.UnitCopy, error)
	NotInStockAmong(ctx context.Context, db DB, ids []uuid.UUID) ([]uuid.UUID, error)

	// InsertAllocations возвращает ошибку, обёрнутую в ErrUnitTaken, при нарушении NFR004.
	InsertAllocations(ctx context.Context, db DB, obligationID uuid.UUID, units []uuid.UUID, now time.Time) error
	ReleaseAllocation(ctx context.Context, db DB, obligationID, unitID uuid.UUID, now time.Time) (bool, error)
	ReleaseAllAllocations(ctx context.Context, db DB, obligationID uuid.UUID, now time.Time) (int, error)
	ConfirmedUnits(ctx context.Context, db DB, obligationID uuid.UUID) ([]uuid.UUID, error)
	ConfirmedAmong(ctx context.Context, db DB, ids []uuid.UUID) ([]uuid.UUID, error)
	ObligationHoldingUnit(ctx context.Context, db DB, unitID uuid.UUID) (uuid.UUID, bool, error)
	IsAllocatedTo(ctx context.Context, db DB, obligationID, unitID uuid.UUID) (bool, error)
	AllocationHistory(ctx context.Context, db DB, unitID uuid.UUID) ([]domain.Allocation, error)
}
