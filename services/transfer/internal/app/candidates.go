package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

func (s *Service) Candidates(ctx context.Context, id uuid.UUID) ([]domain.UnitCopy, error) {
	o, err := s.store.Obligation(ctx, s.pool, id)
	if err != nil {
		return nil, err
	}
	if o.Status != domain.StatusPending && !o.Status.Replaceable() {
		return []domain.UnitCopy{}, nil
	}
	return s.store.Candidates(ctx, s.pool, o, s.clock.Now(), 100)
}
