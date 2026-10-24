package app

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

// Cancel — отмена задания по CancelObligation (заявка отменена, план пересчитан).
// Из PENDING, CONFIRMED и TRANSPORT_REQUESTED → CANCELLED (CAS): закрепления RELEASED, резерв снимается,
// TransferCancelled. Из CONFIRMED и TRANSPORT_REQUESTED ещё CancelTransport: RequestTransport ушла в момент
// подтверждения, и без компенсации заказ, принятый перевозчиком после отмены, остался бы висеть.
// Шлюз отвечает и тогда, когда заказа не было (надгробие).
// Из конечного статуса — идемпотентный отказ без изменений: ни событий, ни аудита.
func (s *Service) Cancel(ctx context.Context, msg *messaging.Message, c CancelObligation) (CancelResult, error) {
	switch c.Reason {
	case transfer.CancelRequestCancelled, transfer.CancelPlanRecalculated, transfer.CancelDutyOfficerDecision:
	default:
		return CancelResult{}, deliveryErr(msg, fmt.Errorf("%w: неизвестная причина отмены %q", ErrInvalidEvent, c.Reason))
	}
	o, err := s.store.Obligation(ctx, s.pool, c.ObligationID)
	if err != nil {
		return CancelResult{}, deliveryErr(msg, err)
	}
	res := CancelResult{ObligationID: o.ID, Status: o.Status}
	err = s.inTx(ctx, msg, func(ctx context.Context, tx pgx.Tx) error {
		r, err := s.cancelLocked(ctx, tx, o, c)
		res = r
		return err
	})
	if err != nil {
		return CancelResult{}, deliveryErr(msg, err)
	}
	return res, nil
}

func (s *Service) cancelLocked(ctx context.Context, tx pgx.Tx, o domain.Obligation, c CancelObligation) (CancelResult, error) {
	now := s.clock.Now()
	// Порядок блокировок тот же, что у подтверждения: Остаток, затем O07.
	if _, err := s.store.LockStock(ctx, tx, o.SourceClinicID, o.ComponentKind); err != nil {
		return CancelResult{}, err
	}
	locked, err := s.store.LockObligation(ctx, tx, o.ID)
	if err != nil {
		return CancelResult{}, err
	}
	if !locked.Status.Cancellable() {
		return CancelResult{ObligationID: o.ID, Status: locked.Status}, nil
	}
	if err := domain.CheckTransition(locked.Status, domain.StatusCancelled); err != nil {
		return CancelResult{}, err
	}
	ok, err := s.store.CASStatus(ctx, tx, locked, domain.StatusCancelled, c.Reason, now)
	if err != nil {
		return CancelResult{}, err
	}
	if !ok {
		return CancelResult{ObligationID: o.ID, Status: locked.Status}, nil
	}
	released := 0
	if locked.Status.HoldsAllocations() {
		if released, err = s.store.ReleaseAllAllocations(ctx, tx, o.ID, now); err != nil {
			return CancelResult{}, err
		}
		if err := s.store.AddReserved(ctx, tx, o.SourceClinicID, o.ComponentKind, -released); err != nil {
			return CancelResult{}, err
		}
	}
	r := refsAt(locked, locked.Version+1)
	if err := s.event(ctx, tx, o.ID, TransferCancelled{
		ObligationID: o.ID, ObligationVersion: r.version, SourceClinicID: r.source, TargetClinicID: r.target,
		RequestID: o.RequestID, Reason: c.Reason,
	}); err != nil {
		return CancelResult{}, err
	}
	if locked.Status.HoldsAllocations() {
		if err := s.command(ctx, tx, o.ID, CancelTransport{ObligationID: o.ID, Reason: logistics.CancelObligationCancelled}); err != nil {
			return CancelResult{}, err
		}
	}
	actor := c.Actor
	if actor == "" {
		actor = actorSystem
	}
	if err := s.audit(ctx, tx, actor, ActionCancelled, o, map[string]any{
		"reason": c.Reason, "from_status": string(locked.Status), "released_units": released,
	}); err != nil {
		return CancelResult{}, err
	}
	return CancelResult{ObligationID: o.ID, Cancelled: true, Status: domain.StatusCancelled}, nil
}

// deliveryErr — для входящего сообщения Kafka ошибки, которые повтор не исправит, становятся постоянными (DLT).
func deliveryErr(msg *messaging.Message, err error) error {
	if msg == nil {
		return err
	}
	return permanentIfInvalid(err)
}
