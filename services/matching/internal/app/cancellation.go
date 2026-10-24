package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5"

	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/matching"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
)

func cancellationRequested(status string) bool {
	return status == "CANCELLED" || status == "CANCELLATION_REQUESTED" || status == "CANCELLATION_REJECTED"
}
func (s *Service) cancellationProcessed(ctx context.Context, msg messaging.Message) error {
	var event transfer.RequestCancellationProcessed
	if err := msg.Decode(&event); err != nil {
		return messaging.Permanent(err)
	}
	if event.RequestID == uuid.Nil || event.At.IsZero() {
		return messaging.Permanent(ErrInvalid)
	}
	return bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		first, err := messaging.FirstTime(ctx, tx, msg, s.clock.Now())
		if err != nil || !first {
			return err
		}
		n, err := s.store.Request(ctx, tx, event.RequestID, true)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if n.Status != "CANCELLATION_REQUESTED" {
			return nil
		}
		actor := Actor{User: msg.Producer, Roles: []string{"SERVICE"}}
		if len(event.NonCancellableObligationIDs) > 0 {
			if err := s.store.SetStatus(ctx, tx, event.RequestID, "CANCELLATION_REJECTED", s.clock.Now()); err != nil {
				return err
			}
			return s.audit(ctx, tx, actor, event.RequestID, "CLINICAL_REQUEST_CANCEL_REJECTED", auditc.ResultError, map[string]any{"non_cancellable_obligation_ids": event.NonCancellableObligationIDs})
		}
		if err := s.store.SetStatus(ctx, tx, event.RequestID, "CANCELLED", s.clock.Now()); err != nil {
			return err
		}
		if err := s.event(ctx, tx, event.RequestID, matching.ClinicalRequestCancelled{ClinicalRequestID: event.RequestID, CancelledBy: n.CreatedBy}); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, event.RequestID, "CLINICAL_REQUEST_CANCELLED", auditc.ResultSuccess)
	})
}
