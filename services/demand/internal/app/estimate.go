package app

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/demand"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/services/demand/internal/domain"
)

type EstimateSelection struct {
	ProfileFileID  uuid.UUID
	ShareVersionID uuid.UUID
	HistoryIDs     []uuid.UUID
	Period         domain.Period
}
type EstimateRecord struct {
	ID          uuid.UUID
	Version     int
	Selection   EstimateSelection
	Status      string
	At          time.Time
	ConfirmedBy string
	Lines       []demand.DemandLine
}

func (s *Service) Calculate(ctx context.Context, in EstimateSelection, actor Actor) (EstimateRecord, error) {
	if in.ProfileFileID == uuid.Nil || in.ShareVersionID == uuid.Nil || len(in.HistoryIDs) == 0 || len(in.HistoryIDs) > 10000 {
		return EstimateRecord{}, fmt.Errorf("%w: выберите профиль, доли и историю", domain.ErrEstimateInput)
	}
	period, err := domain.NewPeriod(in.Period.Start, in.Period.End)
	if err != nil {
		return EstimateRecord{}, fmt.Errorf("%w: период оценки", domain.ErrEstimateInput)
	}
	in.Period = period
	seen := map[uuid.UUID]bool{}
	for _, id := range in.HistoryIDs {
		if id == uuid.Nil || seen[id] {
			return EstimateRecord{}, fmt.Errorf("%w: история повторяется", domain.ErrEstimateInput)
		}
		seen[id] = true
	}
	id, err := uuid.NewV7()
	if err != nil {
		return EstimateRecord{}, err
	}
	n := EstimateRecord{ID: id, Selection: in, Status: "CALCULATED", At: s.clock.Now()}
	err = bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		// Та же блокировка, что у исправления истории: выбранные строки не заменятся до создания ссылок.
		if err := s.store.LockUploads(ctx, tx, domain.FileKindConsumptionHistory); err != nil {
			return err
		}
		inputs, err := s.store.LoadEstimateInput(ctx, tx, in)
		if err != nil {
			return err
		}
		result, err := domain.CalculateEstimate(inputs, s.ref)
		if err != nil {
			return err
		}
		if err := s.store.SaveEstimate(ctx, tx, &n, result); err != nil {
			return err
		}
		for _, line := range result.Clinics {
			n.Lines = append(n.Lines, demand.DemandLine{ClinicID: line.ClinicID, ComponentKind: line.KindCode, BloodGroup: line.BloodGroup, DemandVolumeML: float64(line.VolumeML)})
		}
		if _, err := s.journal.Publish(ctx, tx, contracts.TopicDemandEvents, id.String(), demand.DemandEstimateCalculated{DemandEstimateID: id, VersionNo: n.Version, PeriodStart: in.Period.Start, PeriodEnd: in.Period.End}); err != nil {
			return err
		}
		return s.journal.Audit(ctx, tx, messaging.AuditEntry{UserID: actor.UserID, Role: actor.Role, Action: "ESTIMATE_CALCULATED", ObjectType: "demand_estimate", ObjectID: id.String(), Result: auditc.ResultSuccess, Details: map[string]any{"version": n.Version, "history_rows": len(in.HistoryIDs)}})
	})
	return n, err
}

func (s *Service) Estimate(ctx context.Context, id uuid.UUID) (EstimateRecord, error) {
	return s.store.ReadEstimate(ctx, nil, id, false)
}
func (s *Service) Estimates(ctx context.Context) ([]EstimateRecord, error) {
	return s.store.ListEstimates(ctx)
}

func (s *Service) PublishEstimate(ctx context.Context, id uuid.UUID, actor Actor) (EstimateRecord, error) {
	var n EstimateRecord
	err := bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		n, err = s.store.ReadEstimate(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if n.Status == "PUBLISHED" {
			return nil
		}
		if n.Status != "CALCULATED" {
			return fmt.Errorf("%w: версия не рассчитана", domain.ErrEstimateInput)
		}
		if err := s.store.MarkEstimatePublished(ctx, tx, id, actor.UserID, s.clock.Now()); err != nil {
			return err
		}
		n.Status, n.ConfirmedBy = "PUBLISHED", actor.UserID
		if _, err := s.journal.Publish(ctx, tx, contracts.TopicDemandEvents, id.String(), demand.DemandEstimatePublished{DemandEstimateID: id, VersionNo: n.Version, PeriodStart: n.Selection.Period.Start, PeriodEnd: n.Selection.Period.End, ConfirmedBy: actor.UserID, ClinicShareVersionID: n.Selection.ShareVersionID, Lines: n.Lines}); err != nil {
			return err
		}
		return s.journal.Audit(ctx, tx, messaging.AuditEntry{UserID: actor.UserID, Role: actor.Role, Action: "ESTIMATE_PUBLISHED", ObjectType: "demand_estimate", ObjectID: id.String(), Result: auditc.ResultSuccess})
	})
	return n, err
}
