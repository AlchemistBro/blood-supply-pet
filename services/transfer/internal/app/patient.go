package app

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

func (s *Service) allocatePatient(ctx context.Context, tx pgx.Tx, o domain.Obligation, version int64, units []uuid.UUID) error {
	if o.PatientCaseSealed.IsZero() {
		return nil
	}
	key := uuid.NewSHA1(o.ID, []byte(fmt.Sprintf("allocate:%d", version)))
	if err := s.store.SetPatientDelivery(ctx, tx, o.ID, key, "PENDING"); err != nil {
		return err
	}
	_, err := s.journal.Publish(ctx, tx, contracts.TopicInventoryCommands, o.ID.String(), inventory.AllocateUnitsToPatient{ObligationID: o.ID, RequestID: o.RequestID, IdempotencyKey: key, UnitIDs: units, TargetClinicID: o.TargetClinicID, PatientCaseSealed: string(o.PatientCaseSealed)})
	return err
}
func (s *Service) releasePatient(ctx context.Context, tx pgx.Tx, o domain.Obligation, version int64, reason string) error {
	if o.PatientCaseSealed.IsZero() {
		return nil
	}
	key := uuid.NewSHA1(o.ID, []byte(fmt.Sprintf("release:%d", version)))
	if err := s.store.SetPatientDelivery(ctx, tx, o.ID, key, "RELEASE_REQUESTED"); err != nil {
		return err
	}
	_, err := s.journal.Publish(ctx, tx, contracts.TopicInventoryCommands, o.ID.String(), inventory.ReleasePatientAllocation{ObligationID: o.ID, IdempotencyKey: key, Reason: reason})
	return err
}

func (s *Service) OnPatientAllocationAcknowledged(ctx context.Context, msg *messaging.Message, event inventory.PatientAllocationAcknowledged) error {
	if event.ObligationID == uuid.Nil || event.IdempotencyKey == uuid.Nil || event.AcknowledgedAt.IsZero() {
		return deliveryErr(msg, fmt.Errorf("%w: неверная квитанция ИНТ-05", ErrInvalidEvent))
	}
	return s.inTx(ctx, msg, func(ctx context.Context, tx pgx.Tx) error {
		return s.store.AcknowledgePatient(ctx, tx, event.ObligationID, event.IdempotencyKey, event.AcknowledgedAt)
	})
}
