// Package postgres — хранилище transfer на sqlc-запросах (реализует app.Store).
package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/adapters/postgres/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

// uniqueConfirmedAllocation — частичный уникальный индекс NFR004 (миграция 00100).
const uniqueConfirmedAllocation = "allocation_one_confirmed_per_unit"

// Store реализует app.Store. Состояния не хранит: пул или транзакция приходят в каждом вызове.
type Store struct{}

// New создаёт хранилище.
func New() *Store { return &Store{} }

var _ app.Store = (*Store)(nil)

func q(db app.DB) *gen.Queries { return gen.New(db) }

// InsertObligation вставляет O07; false — choice_id уже есть (повтор выбора).
func (Store) InsertObligation(ctx context.Context, db app.DB, o domain.Obligation) (bool, error) {
	qty, err := int32Of(o.RequestedQty)
	if err != nil {
		return false, err
	}
	n, err := q(db).InsertObligation(ctx, gen.InsertObligationParams{
		ObligationID: o.ID, ChoiceID: o.ChoiceID, RequestID: o.RequestID, Origin: string(o.Origin),
		SourceClinicID: o.SourceClinicID, TargetClinicID: o.TargetClinicID, ComponentKindCode: o.ComponentKind,
		BloodGroupCode: o.BloodGroup, RequestedQty: qty, UrgencyCode: o.UrgencyCode, Deadline: o.Deadline,
		ConfirmWindowSeconds: int64(o.ConfirmWindow / time.Second), FallbackSources: nonNil(o.FallbackSources),
		SpecBloodGroupCode: o.Spec.BloodGroup, SpecRhPositive: o.Spec.RhPositive, SpecPhenotype: o.Spec.Phenotype,
		SpecMinExpiresAt: o.Spec.MinExpiresAt, SpecRuleVersion: o.Spec.RuleVersion, CreatedAt: o.CreatedAt,
	})
	if err != nil {
		return false, fmt.Errorf("вставка обязательства: %w", err)
	}
	return n == 1, nil
}

// Obligation читает O07 без блокировки.
func (Store) Obligation(ctx context.Context, db app.DB, id uuid.UUID) (domain.Obligation, error) {
	row, err := q(db).ObligationByID(ctx, id)
	return obligationOf(row, err, id)
}

// LockObligation читает O07 под FOR UPDATE.
func (Store) LockObligation(ctx context.Context, db app.DB, id uuid.UUID) (domain.Obligation, error) {
	row, err := q(db).LockObligation(ctx, id)
	return obligationOf(row, err, id)
}

// ObligationIDByChoice — id задания по choice_id.
func (Store) ObligationIDByChoice(ctx context.Context, db app.DB, choiceID uuid.UUID) (uuid.UUID, error) {
	id, err := q(db).ObligationIDByChoice(ctx, choiceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("%w: choice_id %s", app.ErrNotFound, choiceID)
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("поиск по choice_id: %w", err)
	}
	return id, nil
}

// ObligationsByRequest — все O07 заявки.
func (Store) ObligationsByRequest(ctx context.Context, db app.DB, requestID uuid.UUID) ([]domain.Obligation, error) {
	rows, err := q(db).ObligationsByRequest(ctx, requestID)
	if err != nil {
		return nil, fmt.Errorf("обязательства заявки: %w", err)
	}
	out := make([]domain.Obligation, 0, len(rows))
	for _, r := range rows {
		o, err := toObligation(r)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

// ObligationsBySource — задания клиники-источника.
func (Store) ObligationsBySource(ctx context.Context, db app.DB, sourceClinicID int64, status *domain.Status, limit int) ([]domain.Obligation, error) {
	l, err := int32Of(limit)
	if err != nil {
		return nil, err
	}
	var st *string
	if status != nil {
		v := string(*status)
		st = &v
	}
	rows, err := q(db).ObligationsBySource(ctx, gen.ObligationsBySourceParams{SourceClinicID: sourceClinicID, Status: st, MaxRows: l})
	if err != nil {
		return nil, fmt.Errorf("обязательства источника: %w", err)
	}
	out := make([]domain.Obligation, 0, len(rows))
	for _, r := range rows {
		o, err := toObligation(r)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

// PendingDueBy — просроченные PENDING по индексу (deadline) WHERE status = 'PENDING'.
func (Store) PendingDueBy(ctx context.Context, db app.DB, now time.Time, limit int) ([]uuid.UUID, error) {
	l, err := int32Of(limit)
	if err != nil {
		return nil, err
	}
	ids, err := q(db).PendingDueBy(ctx, gen.PendingDueByParams{Now: now, MaxRows: l})
	if err != nil {
		return nil, fmt.Errorf("просроченные задания: %w", err)
	}
	return ids, nil
}

// UrgencyWindow — срок подтверждения по справочнику срочности (FR049).
func (Store) UrgencyWindow(ctx context.Context, db app.DB, urgencyCode string) (time.Duration, error) {
	m, err := q(db).UrgencyWindowMinutes(ctx, urgencyCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("%w: неизвестная срочность %q", domain.ErrInvalidObligation, urgencyCode)
	}
	if err != nil {
		return 0, fmt.Errorf("срок по срочности: %w", err)
	}
	return time.Duration(m) * time.Minute, nil
}

// UnknownClinics — клиники, которых нет в справочнике.
func (Store) UnknownClinics(ctx context.Context, db app.DB, ids []int64) ([]int64, error) {
	out, err := q(db).UnknownClinics(ctx, nonNil(ids))
	if err != nil {
		return nil, fmt.Errorf("проверка клиник: %w", err)
	}
	return out, nil
}

// CASStatus — UPDATE … WHERE status = :ожидаемый AND version = :прочитанная.
func (Store) CASStatus(ctx context.Context, db app.DB, o domain.Obligation, next domain.Status, reason string, now time.Time) (bool, error) {
	n, err := q(db).CASStatus(ctx, gen.CASStatusParams{
		Next: string(next), Reason: optString(reason), Now: now, ObligationID: o.ID, Expected: string(o.Status), Version: o.Version,
	})
	if err != nil {
		return false, fmt.Errorf("переход %s → %s: %w", o.Status, next, err)
	}
	return n == 1, nil
}

// CASConfirm — PENDING → CONFIRMED с числом единиц и ключом идемпотентности оператора.
func (Store) CASConfirm(ctx context.Context, db app.DB, o domain.Obligation, qty int, key string, now time.Time) (bool, error) {
	k, err := int32Of(qty)
	if err != nil {
		return false, err
	}
	n, err := q(db).CASConfirm(ctx, gen.CASConfirmParams{
		ConfirmedQty: k, ConfirmKey: optString(key), Now: now, ObligationID: o.ID, Version: o.Version,
	})
	if err != nil {
		return false, fmt.Errorf("подтверждение %s: %w", o.ID, err)
	}
	return n == 1, nil
}

// CASTouch — версия растёт, статус прежний (замена единицы).
func (Store) CASTouch(ctx context.Context, db app.DB, o domain.Obligation, now time.Time) (bool, error) {
	n, err := q(db).CASTouch(ctx, gen.CASTouchParams{Now: now, ObligationID: o.ID, Status: string(o.Status), Version: o.Version})
	if err != nil {
		return false, fmt.Errorf("версия %s: %w", o.ID, err)
	}
	return n == 1, nil
}

// CASTransportRequested — CONFIRMED → TRANSPORT_REQUESTED с номером заказа.
func (Store) CASTransportRequested(ctx context.Context, db app.DB, id uuid.UUID, shipmentRef string, now time.Time) (bool, error) {
	n, err := q(db).CASTransportRequested(ctx, gen.CASTransportRequestedParams{TransportRef: optString(shipmentRef), Now: now, ObligationID: id})
	if err != nil {
		return false, fmt.Errorf("заказ перевозки %s: %w", id, err)
	}
	return n == 1, nil
}

// LockStock создаёт строку Остатка при отсутствии и блокирует её.
func (Store) LockStock(ctx context.Context, db app.DB, clinicID int64, kind string) (domain.Stock, error) {
	key := gen.EnsureStockParams{ClinicID: clinicID, ComponentKindCode: kind}
	if err := q(db).EnsureStock(ctx, key); err != nil {
		return domain.Stock{}, fmt.Errorf("строка остатка %d/%s: %w", clinicID, kind, err)
	}
	row, err := q(db).LockStock(ctx, gen.LockStockParams{ClinicID: clinicID, ComponentKindCode: kind})
	if err != nil {
		return domain.Stock{}, fmt.Errorf("блокировка остатка %d/%s: %w", clinicID, kind, err)
	}
	return domain.Stock{Available: int(row.AvailableUnits), Reserved: int(row.ReservedUnits), Protected: int(row.ProtectedVolume)}, nil
}

// AddReserved меняет резерв на delta.
func (Store) AddReserved(ctx context.Context, db app.DB, clinicID int64, kind string, delta int) error {
	d, err := int32Of(delta)
	if err != nil {
		return err
	}
	if err := q(db).AddReserved(ctx, gen.AddReservedParams{Delta: d, ClinicID: clinicID, ComponentKindCode: kind}); err != nil {
		return fmt.Errorf("резерв %d/%s: %w", clinicID, kind, err)
	}
	return nil
}

// RecountAvailable пересчитывает наличие по копии.
func (Store) RecountAvailable(ctx context.Context, db app.DB, clinicID int64, kind string) error {
	if err := q(db).RecountAvailable(ctx, gen.RecountAvailableParams{ClinicID: clinicID, ComponentKindCode: kind}); err != nil {
		return fmt.Errorf("пересчёт наличия %d/%s: %w", clinicID, kind, err)
	}
	return nil
}

// SetProtectedVolume применяет защищённый объём, если версия новее.
func (Store) SetProtectedVolume(ctx context.Context, db app.DB, clinicID int64, kind string, volume int, version int64, reason string) (bool, error) {
	v, err := int32Of(volume)
	if err != nil {
		return false, err
	}
	n, err := q(db).SetProtectedVolume(ctx, gen.SetProtectedVolumeParams{
		ProtectedVolume: v, ProtectedVersion: version, ProtectedReason: optString(reason), ClinicID: clinicID, ComponentKindCode: kind,
	})
	if err != nil {
		return false, fmt.Errorf("защищённый объём %d/%s: %w", clinicID, kind, err)
	}
	return n == 1, nil
}

// UpsertUnitCopy вставляет или обновляет копию, если версия новее.
func (Store) UpsertUnitCopy(ctx context.Context, db app.DB, u domain.UnitCopy, now time.Time) (bool, error) {
	n, err := q(db).UpsertUnitCopy(ctx, gen.UpsertUnitCopyParams{
		UnitID: u.UnitID, ClinicID: u.ClinicID, ComponentKindCode: u.ComponentKind, BloodGroupCode: u.BloodGroup,
		RhPositive: u.RhPositive, Phenotype: u.Phenotype, VolumeMl: u.VolumeML, ExpiresAt: u.ExpiresAt,
		Status: string(u.Status), Version: u.Version, UpdatedAt: now,
	})
	if err != nil {
		return false, fmt.Errorf("копия единицы %s: %w", u.UnitID, err)
	}
	return n == 1, nil
}

// SetUnitCopyStatus меняет статус копии, если версия новее.
func (Store) SetUnitCopyStatus(ctx context.Context, db app.DB, unitID uuid.UUID, s domain.CopyStatus, reason string, version int64, now time.Time) (bool, error) {
	n, err := q(db).SetUnitCopyStatus(ctx, gen.SetUnitCopyStatusParams{
		Status: string(s), StatusReason: optString(reason), Version: version, Now: now, UnitID: unitID,
	})
	if err != nil {
		return false, fmt.Errorf("статус копии %s: %w", unitID, err)
	}
	return n == 1, nil
}

// UnitCopies — копии единиц по id.
func (Store) UnitCopies(ctx context.Context, db app.DB, ids []uuid.UUID) (map[uuid.UUID]domain.UnitCopy, error) {
	rows, err := q(db).UnitCopies(ctx, nonNil(ids))
	if err != nil {
		return nil, fmt.Errorf("копии единиц: %w", err)
	}
	out := make(map[uuid.UUID]domain.UnitCopy, len(rows))
	for _, r := range rows {
		out[r.UnitID] = domain.UnitCopy{
			UnitID: r.UnitID, ClinicID: r.ClinicID, ComponentKind: r.ComponentKindCode, BloodGroup: r.BloodGroupCode,
			RhPositive: r.RhPositive, Phenotype: r.Phenotype, VolumeML: r.VolumeMl, ExpiresAt: r.ExpiresAt,
			Status: domain.CopyStatus(r.Status), Version: r.Version,
		}
	}
	return out, nil
}

// NotInStockAmong — единицы, которых в копии больше нет в наличии.
func (Store) NotInStockAmong(ctx context.Context, db app.DB, ids []uuid.UUID) ([]uuid.UUID, error) {
	out, err := q(db).NotInStockAmong(ctx, nonNil(ids))
	if err != nil {
		return nil, fmt.Errorf("перепроверка копии: %w", err)
	}
	return out, nil
}

// InsertAllocations ставит закрепления. Нарушение NFR004 возвращается как app.ErrUnitTaken.
func (Store) InsertAllocations(ctx context.Context, db app.DB, obligationID uuid.UUID, units []uuid.UUID, now time.Time) error {
	for _, unitID := range units {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid v7: %w", err)
		}
		err = q(db).InsertAllocation(ctx, gen.InsertAllocationParams{AllocationID: id, UnitID: unitID, ObligationID: obligationID, CreatedAt: now})
		if bspgx.IsUniqueViolation(err) && bspgx.ConstraintName(err) == uniqueConfirmedAllocation {
			return fmt.Errorf("%w: %s: %w", app.ErrUnitTaken, unitID, err)
		}
		if err != nil {
			return fmt.Errorf("закрепление %s: %w", unitID, err)
		}
	}
	return nil
}

// ReleaseAllocation снимает одно действующее закрепление.
func (Store) ReleaseAllocation(ctx context.Context, db app.DB, obligationID, unitID uuid.UUID, now time.Time) (bool, error) {
	n, err := q(db).ReleaseAllocation(ctx, gen.ReleaseAllocationParams{Now: now, ObligationID: obligationID, UnitID: unitID})
	if err != nil {
		return false, fmt.Errorf("снятие закрепления %s: %w", unitID, err)
	}
	return n == 1, nil
}

// ReleaseAllAllocations снимает все действующие закрепления O07 и возвращает их число.
func (Store) ReleaseAllAllocations(ctx context.Context, db app.DB, obligationID uuid.UUID, now time.Time) (int, error) {
	n, err := q(db).ReleaseAllAllocations(ctx, gen.ReleaseAllAllocationsParams{Now: now, ObligationID: obligationID})
	if err != nil {
		return 0, fmt.Errorf("снятие закреплений %s: %w", obligationID, err)
	}
	return int(n), nil
}

// ConfirmedUnits — действующие закрепления O07 по порядку.
func (Store) ConfirmedUnits(ctx context.Context, db app.DB, obligationID uuid.UUID) ([]uuid.UUID, error) {
	out, err := q(db).ConfirmedUnits(ctx, obligationID)
	if err != nil {
		return nil, fmt.Errorf("закреплённые единицы: %w", err)
	}
	return out, nil
}

// ConfirmedAmong — какие из единиц закреплены (за кем угодно).
func (Store) ConfirmedAmong(ctx context.Context, db app.DB, ids []uuid.UUID) ([]uuid.UUID, error) {
	out, err := q(db).ConfirmedAmong(ctx, nonNil(ids))
	if err != nil {
		return nil, fmt.Errorf("занятые единицы: %w", err)
	}
	return out, nil
}

// ObligationHoldingUnit — O07, за которым единица закреплена сейчас.
func (Store) ObligationHoldingUnit(ctx context.Context, db app.DB, unitID uuid.UUID) (uuid.UUID, bool, error) {
	id, err := q(db).ObligationHoldingUnit(ctx, unitID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("закрепление единицы %s: %w", unitID, err)
	}
	return id, true, nil
}

// IsAllocatedTo — единица закреплена за этим O07.
func (Store) IsAllocatedTo(ctx context.Context, db app.DB, obligationID, unitID uuid.UUID) (bool, error) {
	ok, err := q(db).IsAllocatedTo(ctx, gen.IsAllocatedToParams{ObligationID: obligationID, UnitID: unitID})
	if err != nil {
		return false, fmt.Errorf("закрепление %s: %w", unitID, err)
	}
	return ok, nil
}

// AllocationHistory — все закрепления единицы, включая снятые.
func (Store) AllocationHistory(ctx context.Context, db app.DB, unitID uuid.UUID) ([]domain.Allocation, error) {
	rows, err := q(db).AllocationHistory(ctx, unitID)
	if err != nil {
		return nil, fmt.Errorf("история закреплений %s: %w", unitID, err)
	}
	out := make([]domain.Allocation, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.Allocation{
			ID: r.AllocationID, UnitID: r.UnitID, ObligationID: r.ObligationID, Status: domain.AllocationStatus(r.Status),
			CreatedAt: r.CreatedAt, ClosedAt: r.ClosedAt,
		})
	}
	return out, nil
}

func obligationOf(row gen.Obligation, err error, id uuid.UUID) (domain.Obligation, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Obligation{}, fmt.Errorf("%w: %s", app.ErrNotFound, id)
	}
	if err != nil {
		return domain.Obligation{}, fmt.Errorf("чтение обязательства %s: %w", id, err)
	}
	return toObligation(row)
}

func toObligation(r gen.Obligation) (domain.Obligation, error) {
	status, err := domain.ParseStatus(r.Status)
	if err != nil {
		return domain.Obligation{}, err
	}
	return domain.Obligation{
		ID: r.ObligationID, ChoiceID: r.ChoiceID, RequestID: r.RequestID, Origin: domain.Origin(r.Origin),
		SourceClinicID: r.SourceClinicID, TargetClinicID: r.TargetClinicID, ComponentKind: r.ComponentKindCode,
		BloodGroup: r.BloodGroupCode, RequestedQty: int(r.RequestedQty), ConfirmedQty: int(r.ConfirmedQty), Status: status,
		UrgencyCode: r.UrgencyCode, Deadline: r.Deadline, ConfirmWindow: time.Duration(r.ConfirmWindowSeconds) * time.Second,
		FallbackSources: r.FallbackSources,
		Spec: domain.ReplacementSpec{
			BloodGroup: r.SpecBloodGroupCode, RhPositive: r.SpecRhPositive, Phenotype: r.SpecPhenotype,
			MinExpiresAt: r.SpecMinExpiresAt, RuleVersion: r.SpecRuleVersion,
		},
		ConfirmKey: deref(r.ConfirmKey), TransportRef: deref(r.TransportRef), StatusReason: deref(r.StatusReason),
		Version: r.Version, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, nil
}

func int32Of(v int) (int32, error) {
	if v > math.MaxInt32 || v < math.MinInt32 {
		return 0, fmt.Errorf("значение %d вне int32", v)
	}
	return int32(v), nil
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
