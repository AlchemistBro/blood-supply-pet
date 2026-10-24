package app

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

// Reject — явный отказ источника только из PENDING. Порядок блокировок совпадает с Confirm.
func (s *Service) Reject(ctx context.Context, id uuid.UUID, actor string) (bool, error) {
	o, err := s.store.Obligation(ctx, s.pool, id)
	if err != nil {
		return false, err
	}
	rejected := false
	err = s.inTx(ctx, nil, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.store.LockStock(ctx, tx, o.SourceClinicID, o.ComponentKind); err != nil {
			return err
		}
		locked, err := s.store.LockObligation(ctx, tx, id)
		if err != nil {
			return err
		}
		if locked.Status == domain.StatusRejected && locked.StatusReason == transfer.RejectSourceRefused {
			rejected = true
			return nil
		}
		if locked.Status != domain.StatusPending {
			return nil
		}
		ok, err := s.store.CASStatus(ctx, tx, locked, domain.StatusRejected, transfer.RejectSourceRefused, s.clock.Now())
		if err != nil || !ok {
			return err
		}
		r := refsAt(locked, locked.Version+1)
		if err := s.event(ctx, tx, id, TransferRejected{ObligationID: id, RequestID: locked.RequestID, Reason: transfer.RejectSourceRefused, ObligationVersion: r.version, SourceClinicID: r.source, TargetClinicID: r.target}); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, actor, ActionRejected, locked, map[string]any{"reason": transfer.RejectSourceRefused}); err != nil {
			return err
		}
		rejected = true
		return nil
	})
	return rejected, err
}
