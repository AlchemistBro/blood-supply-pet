package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/planning"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/services/planning/internal/domain"
)

type Service struct {
	pool    *pgxpool.Pool
	store   Store
	journal *messaging.Journal
	ref     *refdata.Set
	clock   clock.Clock
}

func New(pool *pgxpool.Pool, store Store, journal *messaging.Journal, ref *refdata.Set, clk clock.Clock) *Service {
	return &Service{pool: pool, store: store, journal: journal, ref: ref, clock: clk}
}
func (s *Service) Plans(ctx context.Context) ([]PlanRecord, error) { return s.store.Plans(ctx) }

func (s *Service) Deficits(ctx context.Context, clinic int64, kind, reason string) ([]DeficitRecord, error) {
	if clinic < 0 || (kind != "" && kind != "PLAN" && kind != "REQUEST") || len(reason) > 64 {
		return nil, ErrInvalid
	}
	return s.store.Deficits(ctx, clinic, kind, reason)
}
func (s *Service) Plan(ctx context.Context, id uuid.UUID) (PlanRecord, error) {
	var n PlanRecord
	err := bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		n, err = s.store.ReadPlan(ctx, tx, id)
		if err != nil {
			return err
		}
		n.Obligations, err = s.store.PlanObligations(ctx, tx, id)
		return err
	})
	return n, err
}

func (s *Service) RebuildProjections(ctx context.Context, actor string) (int64, error) {
	var count int64
	err := bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.store.LockBuild(ctx, tx); err != nil {
			return err
		}
		var err error
		count, err = s.store.RebuildProjections(ctx, tx)
		if err != nil {
			return err
		}
		return s.journal.Audit(ctx, tx, messaging.AuditEntry{UserID: actor, Role: "SERVICE", Action: "PROJECTIONS_REBUILT", ObjectType: "planning_projection", ObjectID: "all", Result: auditc.ResultSuccess, Details: map[string]any{"rows": count}})
	})
	return count, err
}

// Build сохраняет снимок входов и результат вместе с командами и аудитом. Повтор ключа возвращает прежний план.
func (s *Service) Build(ctx context.Context, id uuid.UUID, forecastID *uuid.UUID, actor string, roles ...string) (PlanRecord, error) {
	if id == uuid.Nil {
		return PlanRecord{}, fmt.Errorf("%w: нужен ключ идемпотентности", ErrInvalid)
	}
	var out PlanRecord
	err := bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.store.LockBuild(ctx, tx); err != nil {
			return err
		}
		existing, err := s.store.ReadPlan(ctx, tx, id)
		if err == nil {
			out = existing
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		input, err := s.inputSnapshot(ctx, tx, id)
		if err != nil {
			return err
		}
		if forecastID != nil {
			wire, err := s.store.Forecast(ctx, tx, *forecastID)
			if err != nil {
				return err
			}
			forecast, err := s.domainForecast(*forecastID, wire)
			if err != nil {
				return err
			}
			input.Forecast = &forecast
			if wire.PeriodStart.Before(input.Demand.PeriodStart) || wire.PeriodEnd.After(input.Demand.PeriodEnd) || !input.Demand.PeriodEnd.After(input.Demand.PeriodStart) {
				return fmt.Errorf("%w: период прогноза должен входить в период опубликованной оценки потребности", ErrInvalid)
			}
			ratio := float64(wire.PeriodEnd.Sub(wire.PeriodStart)) / float64(input.Demand.PeriodEnd.Sub(input.Demand.PeriodStart))
			for i := range input.Background {
				input.Background[i].VolumeML *= ratio
			}
		}
		started := s.clock.Now()
		plan, err := domain.NewPlanner(s.clock).Build(input.Input)
		if err != nil {
			return err
		}
		out = PlanRecord{Plan: plan, ElapsedMS: max(s.clock.Now().Sub(started).Milliseconds(), 0)}
		if err := s.store.SavePlan(ctx, tx, input.Input, out); err != nil {
			return err
		}
		previous, err := s.store.SupersedePlans(ctx, tx, id)
		if err != nil {
			return err
		}
		for _, old := range previous {
			if _, err := s.journal.Publish(ctx, tx, contracts.TopicTransferCommands, old.String(), transfer.CancelRequestObligations{RequestID: old, Reason: transfer.CancelPlanRecalculated, OnlyPending: true}); err != nil {
				return err
			}
		}
		version, err := s.store.NextVersion(ctx, tx)
		if err != nil {
			return err
		}
		if err := s.publishPlan(ctx, tx, input.Input, out, forecastID, version); err != nil {
			return err
		}
		role := "SERVICE"
		if len(roles) > 0 {
			role = roles[0]
		}
		return s.journal.Audit(ctx, tx, messaging.AuditEntry{UserID: actor, Role: role, Action: "PLAN_BUILT", ObjectType: "replenishment_plan", ObjectID: id.String(), Result: auditc.ResultSuccess})
	})
	return out, err
}

func (s *Service) publishPlan(ctx context.Context, tx pgx.Tx, input domain.Input, out PlanRecord, forecastID *uuid.UUID, version int64) error {
	plan, id := out.Plan, out.Plan.PlanID
	for _, volume := range plan.Protected {
		if volume.PublishedUnits > math.MaxInt32 {
			return fmt.Errorf("%w: защищённый остаток превышает поддерживаемый объём", ErrInvalid)
		}
		event := planning.ProtectedVolumeCalculated{ClinicID: volume.ClinicID, ComponentKind: volume.Kind, VolumeUnits: volume.PublishedUnits, ReplenishmentPlanID: id, Version: version, CalculatedAt: plan.CreatedAt, Reason: "План " + id.String() + ", норматив и фон"}
		if _, err := s.journal.Publish(ctx, tx, contracts.TopicPlanningEvents, fmt.Sprintf("%d:%s", volume.ClinicID, volume.Kind), event); err != nil {
			return err
		}
	}
	for _, proposal := range plan.Proposals {
		event := planning.TransferProposed{ProposalID: proposal.ProposalID, ReplenishmentPlanID: id, SourceClinicID: proposal.SourceClinicID, TargetClinicID: proposal.TargetClinicID, ComponentKind: proposal.Kind, BloodGroup: proposal.Group, Units: proposal.Units, DeliveryMinutes: proposal.DeliveryMinutes, FallbackSources: proposal.FallbackSources}
		if _, err := s.journal.Publish(ctx, tx, contracts.TopicPlanningEvents, proposal.ProposalID.String(), event); err != nil {
			return err
		}
		group := proposal.Group
		minimum := s.clock.Now().Add(time.Duration(proposal.DeliveryMinutes)*time.Minute + input.Params.MinShelfLifeAtArrival)
		urgency, window := transfer.UrgencyPlanned, int64(86400)
		if input.Forecast != nil {
			urgency, window = transfer.UrgencyEmergency, 900
		}
		command := transfer.OpenObligation{ChoiceID: proposal.ProposalID, RequestID: id, Origin: transfer.OriginPlanProposal, SourceClinicID: proposal.SourceClinicID, TargetClinicID: proposal.TargetClinicID, ComponentKind: proposal.Kind, RequestedQty: proposal.Units, Urgency: urgency, ConfirmWindowSeconds: window, FallbackSources: proposal.FallbackSources, Spec: transfer.ReplacementSpec{BloodGroup: &group, MinExpiresAt: &minimum, RuleVersion: domain.AlgorithmVersion}}
		command.ProtectedFloors = map[int64]int{}
		for _, floor := range plan.Protected {
			if floor.Kind == proposal.Kind {
				command.ProtectedFloors[floor.ClinicID] = int(floor.PublishedUnits)
			}
		}
		if _, err := s.journal.Publish(ctx, tx, contracts.TopicTransferCommands, proposal.ProposalID.String(), command); err != nil {
			return err
		}
	}
	for _, deficit := range plan.Deficits {
		if err := s.store.RecordDeficit(ctx, tx, DeficitRecord{ID: deficit.DeficitID, Type: "PLAN", OriginID: id, ClinicID: deficit.ClinicID, Kind: deficit.Kind, Group: deficit.Group, ML: deficit.UncoveredML, Reason: deficit.Reason, At: plan.CreatedAt}); err != nil {
			return err
		}
		event := planning.PlanDeficitRecorded{DeficitRecordID: deficit.DeficitID, ReplenishmentPlanID: id, ClinicID: deficit.ClinicID, ComponentKind: deficit.Kind, BloodGroup: deficit.Group, UncoveredVolumeML: float64(deficit.UncoveredML), Reason: deficit.Reason}
		if _, err := s.journal.Publish(ctx, tx, contracts.TopicPlanningEvents, deficit.DeficitID.String(), event); err != nil {
			return err
		}
	}
	deficits := len(plan.Deficits)
	built := planning.ReplenishmentPlanBuilt{ReplenishmentPlanID: id, DemandEstimateID: input.Versions.DemandEstimateID, DemandEstimateVersionNo: input.Versions.DemandVersionNo, EmergencyForecastID: forecastID, StockWatermark: input.Versions.StockWatermark, StockSyncedAt: &input.Versions.StockSyncedAt, CoverageRatio: plan.CoverageRatio, TargetsCount: len(plan.Targets), ProposalsCount: len(plan.Proposals), DeficitsCount: &deficits, ElapsedMS: out.ElapsedMS}
	if input.Forecast != nil {
		built.ScenarioVersion = input.Forecast.ScenarioVersion
	}
	_, err := s.journal.Publish(ctx, tx, contracts.TopicPlanningEvents, id.String(), built)
	return err
}
