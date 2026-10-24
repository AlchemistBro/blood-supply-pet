package app

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
)

type NormRecord struct {
	ID        uuid.UUID `json:"stock_norm_id"`
	ClinicID  int64     `json:"clinic_id"`
	Kind      string    `json:"component_kind"`
	Year      int       `json:"consumption_year"`
	Annual    int64     `json:"annual_volume_ml"`
	MonthlyML float64   `json:"avg_monthly_ml"`
	NormML    float64   `json:"norm_ml"`
	Minimum   bool      `json:"is_min_unit_applied"`
	Version   int64     `json:"version"`
	At        time.Time `json:"calculated_at"`
}
type NormThreshold struct {
	ClinicID int64
	Kind     string
	StockML  int64
	NormML   float64
}

func (s *Service) SetAnnualConsumption(ctx context.Context, actor Actor, clinicID int64, kind string, year int, annual int64) (NormRecord, error) {
	component, ok := s.ref.Kind(kind)
	if !ok || !s.ClinicExists(clinicID) || year < 2000 || year > 2100 {
		return NormRecord{}, fmt.Errorf("%w: неверная клиника, вид или год", ErrInvalid)
	}
	value, err := domain.CalculateNorm(annual, component.TypicalVolumeML)
	if err != nil {
		return NormRecord{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return NormRecord{}, err
	}
	annualID, err := uuid.NewV7()
	if err != nil {
		return NormRecord{}, err
	}
	n := NormRecord{ID: id, ClinicID: clinicID, Kind: kind, Year: year, Annual: annual, MonthlyML: value.MonthlyML, NormML: value.NormML, Minimum: value.Minimum, At: s.clock.Now()}
	err = bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		version, err := s.store.NextNormVersion(ctx, tx)
		if err != nil {
			return err
		}
		n.Version = version
		if err := s.store.SaveNorm(ctx, tx, n, annualID, actor.UserID); err != nil {
			return err
		}
		if _, err := s.journal.Publish(ctx, tx, contracts.TopicInventoryEvents, fmt.Sprintf("%d:%s", clinicID, kind), inventory.StockNormPublished{ClinicID: clinicID, ComponentKind: kind, NormML: n.NormML, AvgMonthlyML: n.MonthlyML, IsMinUnitApplied: &n.Minimum, Version: n.Version, CalculatedAt: n.At}); err != nil {
			return err
		}
		if err := s.checkNorms(ctx, tx); err != nil {
			return err
		}
		return s.journal.Audit(ctx, tx, messaging.AuditEntry{UserID: actor.UserID, Role: actor.Role, ClinicID: &clinicID, Action: "CALCULATE_STOCK_NORM", ObjectType: "stock_norm", ObjectID: id.String(), Result: auditc.ResultSuccess})
	})
	return n, err
}

func (s *Service) Norms(ctx context.Context, clinicID int64) ([]NormRecord, error) {
	return s.store.Norms(ctx, clinicID)
}

func (s *Service) checkNorms(ctx context.Context, tx pgx.Tx) error {
	items, err := s.store.CheckNormThresholds(ctx, tx, s.clock.Now())
	if err != nil {
		return err
	}
	for _, item := range items {
		if _, err := s.journal.Publish(ctx, tx, contracts.TopicInventoryEvents, fmt.Sprintf("%d:%s", item.ClinicID, item.Kind), inventory.StockBelowNorm{ClinicID: item.ClinicID, ComponentKind: item.Kind, StockVolumeML: item.StockML, NormML: item.NormML, DetectedAt: s.clock.Now()}); err != nil {
			return err
		}
	}
	return nil
}
