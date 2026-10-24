package app

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

func (s *Service) reviewHoldingUnit(ctx context.Context, tx pgx.Tx, id uuid.UUID, now time.Time) error {
	obligation, held, err := s.store.ObligationHoldingUnit(ctx, tx, id)
	if err != nil || !held {
		return err
	}
	o, err := s.store.Obligation(ctx, tx, obligation)
	if err != nil {
		return err
	}
	copies, err := s.store.UnitCopies(ctx, tx, []uuid.UUID{id})
	if err != nil {
		return err
	}
	unit, ok := copies[id]
	if !ok {
		return nil
	}
	if reason, broken := domain.BreakReasonFor(unit.SourceStatus); broken {
		return s.breakHolding(ctx, tx, id, reason, string(unit.SourceStatus), now)
	}
	if unit.ClinicID != o.SourceClinicID {
		return s.breakHolding(ctx, tx, id, domain.BreakUnitWithdrawn, transfer.BrokenTransferred, now)
	}
	if !unit.ExpiresAt.After(now) {
		return s.breakHolding(ctx, tx, id, domain.BreakUnitWithdrawn, transfer.BrokenExpired, now)
	}
	if o.Spec.Mismatch(o.SourceClinicID, o.ComponentKind, unit) != "" {
		return s.breakHolding(ctx, tx, id, domain.BreakUnitWithdrawn, transfer.BrokenSpecificationChanged, now)
	}
	return nil
}
