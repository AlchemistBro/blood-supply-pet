package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

// Confirm — подтверждение задания оператором источника (FR048, obligation-states.md, «порядок шагов»).
//
//  1. Задание ещё PENDING? Иначе повтор с тем же ключом → тот же Confirmed, прочее → TaskWithdrawn.
//  2. Проверка выбора: разные единицы, не больше requested_qty, все у источника и того же вида.
//  3. Статус единиц в АИСТ-М через inventory — синхронно, до транзакции и вне блокировок (ADR-0004).
//  4. Все недоступны → REJECTED; часть недоступна без согласия на частичное → UnitsUnavailable.
//  5. Транзакция: блокировка source_stock, затем obligation; перепроверка копии; NFR013; CAS PENDING → CONFIRMED;
//     закрепления под UNIQUE (unit_id) WHERE CONFIRMED (NFR004); резерв; outbox; аудит.
//
// Ответ оператор получает в том же запросе; хранимого состояния «проверяется» нет.
func (s *Service) Confirm(ctx context.Context, c ConfirmTransfer) (domain.ConfirmationResult, error) {
	o, err := s.store.Obligation(ctx, s.pool, c.ObligationID)
	if errors.Is(err, ErrNotFound) {
		return domain.InvalidRequest{Reason: "задание не найдено"}, nil
	}
	if err != nil {
		return nil, err
	}
	if o.Status != domain.StatusPending {
		return s.notPending(ctx, s.pool, o, c.IdempotencyKey)
	}
	if inv := domain.ValidateUnitSelection(o.RequestedQty, c.UnitIDs); inv != nil {
		return *inv, nil
	}
	copies, err := s.store.UnitCopies(ctx, s.pool, c.UnitIDs)
	if err != nil {
		return nil, err
	}
	if inv := domain.ValidateOwnership(o.SourceClinicID, o.ComponentKind, c.UnitIDs, copies); inv != nil {
		return *inv, nil
	}

	report, err := s.checker.CheckUnits(ctx, c.UnitIDs)
	if err != nil || report.FromCopy {
		return s.sourceUnavailable(ctx, o, c.Operator, err)
	}
	present, unavailable := domain.SplitByStatus(c.UnitIDs, report.Statuses)
	switch domain.DecideAfterSourceCheck(present, unavailable, c.AcceptPartial) {
	case domain.DecideReject:
		return s.reject(ctx, o, c)
	case domain.DecideUnitsUnavailable:
		return domain.UnitsUnavailable{Units: unavailable}, nil
	case domain.DecideProceed:
	}

	var res domain.ConfirmationResult
	err = s.inTx(ctx, nil, func(ctx context.Context, tx pgx.Tx) error {
		r, err := s.confirmLocked(ctx, tx, o, present, c)
		res = r
		return err
	})
	if errors.Is(err, ErrUnitTaken) {
		// Транзакция откатилась целиком: ни закреплений, ни резерва, ни событий.
		taken, qerr := s.store.ConfirmedAmong(ctx, s.pool, present)
		if qerr != nil {
			return nil, qerr
		}
		if len(taken) == 0 {
			taken = present
		}
		return domain.UnitsTaken{Units: taken}, nil
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Service) confirmLocked(ctx context.Context, tx pgx.Tx, o domain.Obligation, candidates []uuid.UUID,
	c ConfirmTransfer) (domain.ConfirmationResult, error) {
	now := s.clock.Now()
	stock, err := s.store.LockStock(ctx, tx, o.SourceClinicID, o.ComponentKind)
	if err != nil {
		return nil, err
	}
	locked, err := s.store.LockObligation(ctx, tx, o.ID)
	if err != nil {
		return nil, err
	}
	if locked.Status != domain.StatusPending {
		// Таймер, отмена или параллельный повтор того же запроса успели раньше.
		return s.notPending(ctx, tx, locked, c.IdempotencyKey)
	}

	// Копия могла узнать о выдаче или карантине, пока шла проверка в АИСТ-М (гонка 2): перепроверка под блокировкой.
	notInStock, err := s.store.NotInStockAmong(ctx, tx, candidates)
	if err != nil {
		return nil, err
	}
	present, unavailable := domain.RecheckUnderLock(candidates, notInStock, c.AcceptPartial)
	if unavailable != nil {
		return *unavailable, nil
	}

	k := len(present)
	if exceeded := stock.CheckProtected(k); exceeded != nil {
		return *exceeded, nil
	}
	ok, err := s.store.CASConfirm(ctx, tx, locked, k, c.IdempotencyKey, now)
	if err != nil {
		return nil, err
	}
	if !ok {
		return domain.TaskWithdrawn{ObligationID: o.ID, Status: locked.Status}, nil
	}
	if err := s.store.InsertAllocations(ctx, tx, o.ID, present, now); err != nil {
		return nil, err
	}
	if err := s.store.AddReserved(ctx, tx, o.SourceClinicID, o.ComponentKind, k); err != nil {
		return nil, err
	}

	refs := refsAt(locked, locked.Version+1)
	if err := s.event(ctx, tx, o.ID, TransferConfirmed{
		ObligationID: o.ID, ObligationVersion: refs.version, SourceClinicID: refs.source, TargetClinicID: refs.target, RequestID: o.RequestID, Units: present, Quantity: k, Operator: c.Operator,
	}); err != nil {
		return nil, err
	}
	shortfall := domain.Shortfall(o.RequestedQty, k)
	if shortfall > 0 {
		// Недопоставка: подбор или план предложит следующий источник только на остаток.
		if err := s.event(ctx, tx, o.ID, TransferShortfallRecorded{
			ObligationID: o.ID, ObligationVersion: refs.version, SourceClinicID: refs.source, TargetClinicID: refs.target, RequestID: o.RequestID, ComponentKind: o.ComponentKind, Shortfall: shortfall,
		}); err != nil {
			return nil, err
		}
	}
	regime, ok := logistics.TemperatureRegimeFor(o.ComponentKind)
	if !ok {
		return nil, fmt.Errorf("нет температурного режима перевозки для вида %q", o.ComponentKind)
	}
	// Следующий шаг саги — заказ перевозки. Команда в той же транзакции: подтверждение без заказа
	// или заказ без подтверждения невозможны (ADR-0003).
	if err := s.command(ctx, tx, o.ID, RequestTransport{
		ObligationID: o.ID, RequestID: o.RequestID, SourceClinicID: o.SourceClinicID, TargetClinicID: o.TargetClinicID,
		ComponentKind: o.ComponentKind, Quantity: k, TemperatureRegime: regime,
	}); err != nil {
		return nil, err
	}
	if err := s.audit(ctx, tx, c.Operator, ActionConfirmed, o, map[string]any{
		"confirmed_qty": k, "requested_qty": o.RequestedQty, "units": present,
	}); err != nil {
		return nil, err
	}
	return domain.Confirmed{ObligationID: o.ID, Units: present, Shortfall: shortfall}, nil
}

// notPending — ответ, когда задание уже не ждёт подтверждения: повтор того же запроса или «задание снято».
func (s *Service) notPending(ctx context.Context, db DB, o domain.Obligation, key string) (domain.ConfirmationResult, error) {
	if !domain.IsConfirmReplay(o.Status, o.ConfirmKey, key) {
		return domain.TaskWithdrawn{ObligationID: o.ID, Status: o.Status}, nil
	}
	units, err := s.store.ConfirmedUnits(ctx, db, o.ID)
	if err != nil {
		return nil, err
	}
	return domain.Confirmed{ObligationID: o.ID, Units: units, Shortfall: domain.Shortfall(o.RequestedQty, o.ConfirmedQty)}, nil
}

// reject — все выбранные единицы недоступны: PENDING → REJECTED. Меняется только строка O07, Остаток не трогается.
func (s *Service) reject(ctx context.Context, o domain.Obligation, c ConfirmTransfer) (domain.ConfirmationResult, error) {
	var res domain.ConfirmationResult
	err := s.inTx(ctx, nil, func(ctx context.Context, tx pgx.Tx) error {
		locked, err := s.store.LockObligation(ctx, tx, o.ID)
		if err != nil {
			return err
		}
		if locked.Status != domain.StatusPending {
			res, err = s.notPending(ctx, tx, locked, c.IdempotencyKey)
			return err
		}
		reason := domain.RejectUnitsUnavailable
		ok, err := s.store.CASStatus(ctx, tx, locked, domain.StatusRejected, string(reason), s.clock.Now())
		if err != nil {
			return err
		}
		if !ok {
			res = domain.TaskWithdrawn{ObligationID: o.ID, Status: locked.Status}
			return nil
		}
		r := refsAt(locked, locked.Version+1)
		if err := s.event(ctx, tx, o.ID, TransferRejected{
			ObligationID: o.ID, ObligationVersion: r.version, SourceClinicID: r.source, TargetClinicID: r.target,
			RequestID: o.RequestID, Reason: string(reason),
		}); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, c.Operator, ActionRejected, o, map[string]any{"reason": string(reason)}); err != nil {
			return err
		}
		res = domain.Rejected{ObligationID: o.ID, Reason: reason}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// sourceUnavailable — АИСТ-М не ответила или inventory ответил из копии. Вслепую не закрепляем: плановое
// задание остаётся PENDING («повторите»), экстренное — то же плюс эскалация дежурному (допущение этапа 1:
// BS_ALLOW_COPY_CONFIRM_EMERGENCY не реализован, подтверждение по копии появится только с отдельным ADR).
func (s *Service) sourceUnavailable(ctx context.Context, o domain.Obligation, operator string, cause error) (domain.ConfirmationResult, error) {
	s.log.WarnContext(ctx, "статус единиц в АИСТ-М не получен, подтверждение отклонено",
		"obligation_id", o.ID, "urgency", o.UrgencyCode, "err", cause)
	if o.UrgencyCode != domain.UrgencyEmergency {
		return domain.SourceSystemUnavailable{}, nil
	}
	// Статус O07 не меняется; эскалация и запись аудита — одна отдельная транзакция. Повторы оператора
	// не плодят эскалаций: одна на задание, пока оно PENDING (escalated_at).
	err := s.inTx(ctx, nil, func(ctx context.Context, tx pgx.Tx) error {
		first, err := s.store.MarkEscalated(ctx, tx, o.ID, s.clock.Now())
		if err != nil || !first {
			return err
		}
		reason := string(domain.EscalationSourceSystemUnavailable)
		r := refsAt(o, o.Version)
		if err := s.event(ctx, tx, o.ID, EscalationRaised{
			ObligationID: o.ID, ObligationVersion: r.version, SourceClinicID: r.source, TargetClinicID: r.target, RequestID: o.RequestID, Reason: reason, At: s.clock.Now(),
		}); err != nil {
			return err
		}
		return s.audit(ctx, tx, operator, ActionEscalated, o, map[string]any{"reason": reason})
	})
	if err != nil {
		return nil, err
	}
	return domain.SourceSystemUnavailable{}, nil
}
