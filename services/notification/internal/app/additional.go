package app

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/security"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/domain"
)

func (s *Service) handleAdditional(ctx context.Context, msg messaging.Message) error {
	switch msg.Type {
	case "TransferRejected", "TransferExpired", "TransferCancelled":
		var e struct {
			ObligationID uuid.UUID `json:"obligation_id"`
			RequestID    uuid.UUID `json:"request_id"`
			Reason       string    `json:"reason"`
		}
		if err := msg.Decode(&e); err != nil {
			return messaging.Permanent(err)
		}
		if e.ObligationID == uuid.Nil {
			return messaging.Permanent(fmt.Errorf("%s без obligation_id", msg.Type))
		}
		return s.inTx(ctx, msg, func(ctx context.Context, tx pgx.Tx) error {
			o, err := s.Store.FindObligation(ctx, tx, e.ObligationID)
			if err != nil {
				return err
			}
			return s.enqueueTerminal(ctx, tx, msg, e.ObligationID, e.RequestID, msg.Type+": "+e.Reason, o)
		})
	case "StockBelowNorm":
		var e inventory.StockBelowNorm
		if err := msg.Decode(&e); err != nil {
			return messaging.Permanent(err)
		}
		if e.ClinicID <= 0 || e.ComponentKind == "" {
			return messaging.Permanent(fmt.Errorf("некорректный StockBelowNorm"))
		}
		return s.inTx(ctx, msg, func(ctx context.Context, tx pgx.Tx) error {
			return s.enqueue(ctx, tx, msg, domain.Draft{Kind: domain.KindStockBelowNorm, To: domain.Recipient{Role: domain.RoleOperator, ClinicID: &e.ClinicID}, Facts: domain.Facts{SourceClinicID: &e.ClinicID, ComponentKind: e.ComponentKind}})
		})
	case "SecurityIncidentSuspected":
		var e security.SecurityIncidentSuspected
		if err := msg.Decode(&e); err != nil {
			return messaging.Permanent(err)
		}
		if e.IncidentID == uuid.Nil || e.Rule == "" {
			return messaging.Permanent(fmt.Errorf("некорректный SecurityIncidentSuspected"))
		}
		return s.inTx(ctx, msg, func(ctx context.Context, tx pgx.Tx) error {
			return s.enqueue(ctx, tx, msg, domain.Draft{Kind: domain.KindSecurityIncident, To: domain.Recipient{Role: domain.RoleIT}, Facts: domain.Facts{Reason: e.Rule + " / " + e.IncidentID.String() + " / " + e.DetectedAt.UTC().Format("2006-01-02T15:04:05Z")}})
		})
	default:
		return nil
	}
}

func (s *Service) enqueueTerminal(ctx context.Context, tx pgx.Tx, msg messaging.Message, id, request uuid.UUID, reason string, o *domain.Obligation) error {
	// Ключ O07 сохраняет порядок с ObligationOpened: отсутствие проекции означает повреждённую историю.
	if o == nil {
		return fmt.Errorf("нет проекции задания %s", id)
	}
	facts := domain.OnEscalation(id, request, reason, o).Facts
	recipients := []domain.Recipient{{Role: domain.RoleOperator, ClinicID: &o.SourceClinicID}}
	if o.Origin == domain.OriginDoctorChoice {
		recipients = append(recipients, domain.Recipient{Role: domain.RolePhysician, ClinicID: &o.TargetClinicID})
	}
	for _, recipient := range recipients {
		if err := s.enqueue(ctx, tx, msg, domain.Draft{Kind: domain.KindTransferTerminal, To: recipient, Facts: facts}); err != nil {
			return err
		}
	}
	return nil
}
