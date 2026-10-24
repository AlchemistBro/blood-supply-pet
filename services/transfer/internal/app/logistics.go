package app

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

// OnLogisticsReply — ответ logistics-gateway. Inbox и обработка в одной транзакции: повторная доставка
// ничего не меняет. Блокируется только строка O07, без Остатка: порядок блокировок с подтверждением не нарушается.
func (s *Service) OnLogisticsReply(ctx context.Context, msg *messaging.Message, r LogisticsReply) error {
	err := s.inTx(ctx, msg, func(ctx context.Context, tx pgx.Tx) error {
		now := s.clock.Now()
		o, err := s.store.LockObligation(ctx, tx, r.ObligationID)
		if err != nil {
			return err
		}
		switch r.Kind {
		case ReplyAccepted:
			return s.transportAccepted(ctx, tx, o, r, now)
		case ReplyRejected:
			return s.transportRejected(ctx, tx, o, r)
		case ReplyCancelled:
			return s.audit(ctx, tx, actorLogistics, ActionTransportCancelled, o, nil)
		default:
			return fmt.Errorf("%w: неизвестный ответ логистики %q", ErrInvalidEvent, r.Kind)
		}
	})
	return deliveryErr(msg, err)
}

// transportAccepted — CONFIRMED → TRANSPORT_REQUESTED. Повтор при TRANSPORT_REQUESTED — без изменений.
// Поздний ответ для нарушенного или отменённого O07 не оживляет его: CancelTransport уже ушла, ответ только в аудит.
func (s *Service) transportAccepted(ctx context.Context, tx pgx.Tx, o domain.Obligation, r LogisticsReply, now time.Time) error {
	switch o.Status {
	case domain.StatusConfirmed:
		ok, err := s.store.CASTransportRequested(ctx, tx, o.ID, r.ShipmentRef, now)
		if err != nil || !ok {
			return err
		}
		if err := s.event(ctx, tx, o.ID, TransportRequested{ObligationID: o.ID, ShipmentRef: r.ShipmentRef}); err != nil {
			return err
		}
		return s.audit(ctx, tx, actorLogistics, ActionTransportRequested, o, map[string]any{"shipment_ref": r.ShipmentRef})
	case domain.StatusTransportRequested:
		return nil
	default:
		return s.audit(ctx, tx, actorLogistics, ActionTransportReplyIgnored, o, map[string]any{
			"reply": string(r.Kind), "shipment_ref": r.ShipmentRef, "status": string(o.Status),
		})
	}
}

// transportRejected — O07 остаётся CONFIRMED: единицы закреплены, перевозку организует дежурный.
// REFUSED — перевозчик отказал (TransportRequestFailed + эскалация); OUTCOME_UNKNOWN — исход неизвестен
// (эскалация; если сверка шлюза найдёт заказ, придёт запоздалый TransportAccepted).
func (s *Service) transportRejected(ctx context.Context, tx pgx.Tx, o domain.Obligation, r LogisticsReply) error {
	var escalation domain.EscalationReason
	switch r.Reason {
	case RejectRefused:
		escalation = domain.EscalationTransportRefused
	case RejectOutcomeUnknown:
		escalation = domain.EscalationTransportOutcomeUnknown
	default:
		return fmt.Errorf("%w: неизвестная причина TransportRejected %q", ErrInvalidEvent, r.Reason)
	}
	if o.Status != domain.StatusConfirmed {
		return s.audit(ctx, tx, actorLogistics, ActionTransportReplyIgnored, o, map[string]any{
			"reply": string(r.Kind), "reason": r.Reason, "status": string(o.Status),
		})
	}
	if r.Reason == RejectRefused {
		if err := s.event(ctx, tx, o.ID, TransportRequestFailed{ObligationID: o.ID, Reason: r.Reason, Details: r.Details}); err != nil {
			return err
		}
	}
	if err := s.event(ctx, tx, o.ID, EscalationRaised{
		ObligationID: o.ID, RequestID: o.RequestID, Reason: string(escalation), At: s.clock.Now(),
	}); err != nil {
		return err
	}
	return s.audit(ctx, tx, actorLogistics, ActionTransportFailed, o, map[string]any{"reason": r.Reason, "details": r.Details})
}
