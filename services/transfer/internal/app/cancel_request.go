package app

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

func (s *Service) CancelRequest(ctx context.Context, msg *messaging.Message, c transfer.CancelRequestObligations) error {
	if c.RequestID == uuid.Nil || (c.Reason != transfer.CancelPlanRecalculated && c.Reason != transfer.CancelRequestCancelled) || (c.Reason == transfer.CancelPlanRecalculated && !c.OnlyPending) {
		return deliveryErr(msg, fmt.Errorf("%w: неверная пакетная отмена", ErrInvalidEvent))
	}
	return s.inTx(ctx, msg, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.store.LockRequest(ctx, tx, c.RequestID); err != nil {
			return err
		}
		if err := s.store.CancelRequest(ctx, tx, c.RequestID, c.Reason, s.clock.Now()); err != nil {
			return err
		}
		items, err := s.store.ObligationsByRequest(ctx, tx, c.RequestID)
		if err != nil {
			return err
		}
		keys := make([]stockKey, 0, len(items))
		for _, o := range items {
			keys = append(keys, stockKey{clinicID: o.SourceClinicID, kind: o.ComponentKind})
		}
		slices.SortFunc(keys, compareStockKeys)
		keys = slices.Compact(keys)
		for _, key := range keys {
			if _, err := s.store.LockStock(ctx, tx, key.clinicID, key.kind); err != nil {
				return err
			}
		}
		for _, o := range items {
			if _, err := s.cancelLocked(ctx, tx, o, CancelObligation{ObligationID: o.ID, Reason: c.Reason, OnlyPending: c.OnlyPending, Actor: actorSystem}); err != nil {
				return err
			}
		}
		if c.Reason == transfer.CancelRequestCancelled {
			current, err := s.store.ObligationsByRequest(ctx, tx, c.RequestID)
			if err != nil {
				return err
			}
			blocked := make([]uuid.UUID, 0)
			for _, o := range current {
				if o.Status == domain.StatusDispatched || o.Status == domain.StatusDelivered {
					blocked = append(blocked, o.ID)
				}
			}
			if err := s.event(ctx, tx, c.RequestID, transfer.RequestCancellationProcessed{RequestID: c.RequestID, NonCancellableObligationIDs: blocked, At: s.clock.Now()}); err != nil {
				return err
			}
		}
		return s.journal.Audit(ctx, tx, messaging.AuditEntry{UserID: actorSystem, Action: "REQUEST_OBLIGATIONS_CANCELLED", ObjectType: "request", ObjectID: c.RequestID.String(), Result: auditc.ResultSuccess, Details: map[string]any{"reason": c.Reason, "only_pending": c.OnlyPending}})
	})
}
