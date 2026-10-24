package app

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

// Проекция inventory.events. Каждое событие сначала блокирует те же строки source_stock, что и подтверждение
// (как InventoryProjection прототипа): иначе выдача единицы во время подтверждения даёт рассинхрон резерва и NFR013.
// Строки берутся по копии единицы и по обязательству, за которым она закреплена, а не только по событию
// (lockUnitScope): порядок «Остаток, затем O07» соблюдается, даже если событие и копия расходятся.
// msg — входящее сообщение для inbox (nil при прямом вызове).

// OnUnitRegistered — единица появилась в копии запаса; может прийти сразу на карантине (тогда в наличие не входит).
// Пустой статус — IN_STOCK (событие регистрации без статуса означает «в наличии»).
// Если новая версия переносит единицу в другую клинику или вид, пересчитываются оба Остатка.
func (s *Service) OnUnitRegistered(ctx context.Context, msg *messaging.Message, e UnitRegistered) error {
	if e.UnitID == uuid.Nil || e.ComponentKind == "" {
		return deliveryErr(msg, fmt.Errorf("%w: UnitRegistered без unit_id или вида", ErrInvalidEvent))
	}
	if e.Status == "" {
		e.Status = domain.UnitInStock
	}
	status, err := domain.ParseUnitStatus(string(e.Status))
	if err != nil {
		return deliveryErr(msg, fmt.Errorf("%w: %w", ErrInvalidEvent, err))
	}
	err = s.inTx(ctx, msg, func(ctx context.Context, tx pgx.Tx) error {
		now := s.clock.Now()
		scope, err := s.lockUnitScope(ctx, tx, e.UnitID, stockKey{clinicID: e.ClinicID, kind: e.ComponentKind})
		if err != nil {
			return err
		}
		applied, err := s.store.UpsertUnitCopy(ctx, tx, domain.UnitCopy{
			UnitID: e.UnitID, ClinicID: e.ClinicID, ComponentKind: e.ComponentKind, BloodGroup: e.BloodGroup,
			RhPositive: e.RhPositive, Phenotype: e.Phenotype, VolumeML: e.VolumeML, ExpiresAt: e.ExpiresAt,
			Status: status, Version: e.Version,
		}, now)
		if err != nil || !applied {
			return err // устаревшая версия ничего не меняет
		}
		for _, k := range scope.keys {
			if err := s.store.RecountAvailable(ctx, tx, k.clinicID, k.kind); err != nil {
				return err
			}
		}
		// Повторная регистрация уже закреплённой единицы с недоступным статусом нарушает обязательство так же,
		// как UnitUpdated.
		if breakReason, breaks := domain.BreakReasonFor(status); breaks {
			return s.breakHolding(ctx, tx, e.UnitID, breakReason, string(e.Status), now)
		}
		return nil
	})
	return deliveryErr(msg, err)
}

// OnUnitUpdated — изменился статус единицы. Карантин закреплённой единицы равен выбытию:
// отправлять её нельзя (ПП РФ № 641, п. 62 а), обязательство сразу BROKEN с причиной UNIT_QUARANTINED.
func (s *Service) OnUnitUpdated(ctx context.Context, msg *messaging.Message, e UnitUpdated) error {
	status, err := domain.ParseUnitStatus(string(e.Status))
	if err != nil {
		return deliveryErr(msg, fmt.Errorf("%w: %w", ErrInvalidEvent, err))
	}
	return s.applyUnitStatus(ctx, msg, e.UnitID, e.ClinicID, e.ComponentKind, status, string(e.Status), e.Version)
}

// OnUnitWithdrawn — единица выбыла. Выбытие закреплённой единицы сразу переводит O07 в BROKEN
// (решение владельца 23.10.2026, как в прототипе): замену оператор делает до выбытия.
// Статус копии — код справочника по причине выбытия (EXPIRED — списание), сама причина — в status_reason.
func (s *Service) OnUnitWithdrawn(ctx context.Context, msg *messaging.Message, e UnitWithdrawn) error {
	status, err := domain.WithdrawalStatus(e.Reason)
	if err != nil {
		return deliveryErr(msg, fmt.Errorf("%w: %w", ErrInvalidEvent, err))
	}
	return s.applyUnitStatus(ctx, msg, e.UnitID, e.ClinicID, e.ComponentKind, status, e.Reason, e.Version)
}

func (s *Service) applyUnitStatus(ctx context.Context, msg *messaging.Message, unitID uuid.UUID, clinicID int64, kind string,
	status domain.UnitStatus, reason string, version int64) error {
	if unitID == uuid.Nil || kind == "" {
		return deliveryErr(msg, fmt.Errorf("%w: событие единицы без unit_id или вида", ErrInvalidEvent))
	}
	err := s.inTx(ctx, msg, func(ctx context.Context, tx pgx.Tx) error {
		now := s.clock.Now()
		scope, err := s.lockUnitScope(ctx, tx, unitID, stockKey{clinicID: clinicID, kind: kind})
		if err != nil {
			return err
		}
		if scope.copyKey == nil {
			return nil // единица неизвестна копии: без регистрации статус применить не к чему
		}
		applied, err := s.store.SetUnitCopyStatus(ctx, tx, unitID, status, reason, version, now)
		if err != nil || !applied {
			return err // дубль или устаревшая версия
		}
		if err := s.store.RecountAvailable(ctx, tx, scope.copyKey.clinicID, scope.copyKey.kind); err != nil {
			return err
		}
		if breakReason, breaks := domain.BreakReasonFor(status); breaks {
			return s.breakHolding(ctx, tx, unitID, breakReason, reason, now)
		}
		return nil
	})
	return deliveryErr(msg, err)
}

// breakHolding — компенсация: закреплённая единица недоступна. В той же транзакции O07 → BROKEN, все его
// закрепления RELEASED, резерв уменьшается, TransferBroken и CancelTransport. Команда шлётся всегда:
// RequestTransport уже в пути, а шлюз отвечает и тогда, когда заказа не было (надгробие).
// reason — причина в status_reason O07; unitReason — причина выбытия или статус единицы для TransferBroken.
func (s *Service) breakHolding(ctx context.Context, tx pgx.Tx, unitID uuid.UUID, reason domain.BreakReason, unitReason string,
	now time.Time) error {
	obligationID, held, err := s.store.ObligationHoldingUnit(ctx, tx, unitID)
	if err != nil || !held {
		return err
	}
	o, err := s.store.LockObligation(ctx, tx, obligationID)
	if err != nil {
		return err
	}
	if !o.Status.Breakable() {
		return nil
	}
	ok, err := s.store.CASStatus(ctx, tx, o, domain.StatusBroken, string(reason), now)
	if err != nil || !ok {
		return err
	}
	released, err := s.store.ReleaseAllAllocations(ctx, tx, o.ID, now)
	if err != nil {
		return err
	}
	if err := s.store.AddReserved(ctx, tx, o.SourceClinicID, o.ComponentKind, -released); err != nil {
		return err
	}
	r := refsAt(o, o.Version+1)
	if err := s.event(ctx, tx, o.ID, TransferBroken{
		ObligationID: o.ID, ObligationVersion: r.version, SourceClinicID: r.source, TargetClinicID: r.target, RequestID: o.RequestID, UnitID: unitID, Reason: domain.BrokenEventReason(reason, unitReason),
	}); err != nil {
		return err
	}
	if err := s.command(ctx, tx, o.ID, CancelTransport{ObligationID: o.ID, Reason: reason.CancelTransportReason()}); err != nil {
		return err
	}
	return s.audit(ctx, tx, actorSystem, ActionBroken, o, map[string]any{
		"unit_id": unitID, "reason": string(reason), "unit_reason": unitReason, "from_status": string(o.Status),
		"released_units": released,
	})
}

// OnProtectedVolume — защищённый объём источника (NFR013). Применяется только более новая версия.
func (s *Service) OnProtectedVolume(ctx context.Context, msg *messaging.Message, e ProtectedVolume) error {
	if e.ComponentKind == "" || e.VolumeUnits < 0 {
		return deliveryErr(msg, fmt.Errorf("%w: ProtectedVolumePublished без вида или с отрицательным объёмом", ErrInvalidEvent))
	}
	err := s.inTx(ctx, msg, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.store.LockStock(ctx, tx, e.ClinicID, e.ComponentKind); err != nil {
			return err
		}
		_, err := s.store.SetProtectedVolume(ctx, tx, e.ClinicID, e.ComponentKind, e.VolumeUnits, e.Version, e.Reason)
		return err
	})
	return deliveryErr(msg, err)
}
