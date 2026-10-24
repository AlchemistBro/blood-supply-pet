package app

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/int02"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/planning"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/services/planning/internal/domain"
)

var scenarioVersion = regexp.MustCompile(`^[a-zA-Z0-9._:-]{1,128}$`)

func (s *Service) domainForecast(id uuid.UUID, f int02.EmergencyForecast) (domain.Forecast, error) {
	out := domain.Forecast{ForecastID: id, ScenarioVersion: f.ScenarioVersion}
	if !scenarioVersion.MatchString(f.ScenarioVersion) || len(f.Lines) == 0 || len(f.Lines) > 10000 || !f.PeriodEnd.After(f.PeriodStart) || f.PeriodEnd.Sub(f.PeriodStart) > 366*24*time.Hour {
		return out, fmt.Errorf("%w: сценарий, период или строки прогноза", ErrInvalid)
	}
	for _, district := range f.Territory.Districts {
		found := false
		if math.IsNaN(district.Weight) || math.IsInf(district.Weight, 0) || district.Weight <= 0 {
			return out, fmt.Errorf("%w: вес района", ErrInvalid)
		}
		for _, ref := range s.ref.Districts {
			if ref.DistrictCode == district.DistrictCode {
				out.Territory = append(out.Territory, domain.DistrictWeight{DistrictID: ref.DistrictID, Weight: district.Weight})
				found = true
				break
			}
		}
		if !found {
			return out, fmt.Errorf("%w: неизвестный район", ErrInvalid)
		}
	}
	for i, line := range f.Lines {
		kind, ok := s.ref.Kind(line.ComponentKind)
		if !ok || line.VolumeML < 0 || line.VolumeML > 1_000_000_000 {
			return out, fmt.Errorf("%w: вид или объём строки прогноза", ErrInvalid)
		}
		if line.BloodGroup != "" {
			known := false
			for _, group := range s.ref.BloodGroups {
				if group.Code == line.BloodGroup {
					known = true
				}
			}
			if !known || !kind.IsGroupApplicable {
				return out, fmt.Errorf("%w: группа прогноза", ErrInvalid)
			}
		}
		clinic := int64(0)
		if line.ClinicCode != "" {
			for _, ref := range s.ref.Clinics {
				if ref.ClinicCode == line.ClinicCode && ref.IsActive {
					clinic = ref.ClinicID
					break
				}
			}
			if clinic == 0 {
				return out, fmt.Errorf("%w: клиника прогноза", ErrInvalid)
			}
		}
		if clinic == 0 && len(out.Territory) == 0 {
			return out, fmt.Errorf("%w: нет районов для распределения прогноза", ErrInvalid)
		}
		if line.Share != nil && (*line.Share < 0 || *line.Share > 1 || math.IsNaN(*line.Share) || math.IsInf(*line.Share, 0)) {
			return out, fmt.Errorf("%w: доля строки", ErrInvalid)
		}
		out.Lines = append(out.Lines, domain.ForecastLine{LineID: uuid.NewSHA1(id, []byte(strconv.Itoa(i))), ClinicID: clinic, Kind: line.ComponentKind, Group: line.BloodGroup, VolumeML: float64(line.VolumeML)})
	}
	return out, nil
}

func (s *Service) AcceptForecast(ctx context.Context, f int02.EmergencyForecast, actor string) (uuid.UUID, error) {
	return s.acceptForecast(ctx, f, actor, "FORECAST_ACCEPTED", nil)
}

func (s *Service) acceptForecast(ctx context.Context, f int02.EmergencyForecast, actor, action string, details map[string]any) (uuid.UUID, error) {
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("blood-supply:forecast:"+f.ScenarioVersion))
	forecast, err := s.domainForecast(id, f)
	if err != nil {
		return uuid.Nil, err
	}
	err = bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		inserted, err := s.store.InsertForecast(ctx, tx, id, f, s.clock.Now())
		if err != nil || !inserted {
			return err
		}
		rough := false
		for _, line := range forecast.Lines {
			rough = rough || line.ClinicID == 0
		}
		if _, err := s.journal.Publish(ctx, tx, contracts.TopicPlanningEvents, id.String(), planning.EmergencyForecastAccepted{EmergencyForecastID: id, ScenarioVersion: f.ScenarioVersion, Territory: f.Territory.Name, PeriodStart: f.PeriodStart, PeriodEnd: f.PeriodEnd, IsRoughEstimate: rough, LinesCount: len(f.Lines)}); err != nil {
			return err
		}
		role := "SERVICE"
		if action == "FORECAST_ADJUSTED" {
			role = "CLINIC_OPERATOR"
		}
		return s.journal.Audit(ctx, tx, messaging.AuditEntry{UserID: actor, Role: role, Action: action, ObjectType: "emergency_forecast", ObjectID: id.String(), Result: auditc.ResultSuccess, Details: details})
	})
	return id, err
}

func (s *Service) Forecasts(ctx context.Context) ([]ForecastRecord, error) {
	return s.store.Forecasts(ctx)
}
func (s *Service) Forecast(ctx context.Context, id uuid.UUID) (int02.EmergencyForecast, error) {
	return s.store.Forecast(ctx, nil, id)
}

// AdjustForecast создаёт новую версию: опубликованные планы сохраняют исходный прогноз.
func (s *Service) AdjustForecast(ctx context.Context, id, lineID uuid.UUID, volume int64, clinic, reason, actor string) (uuid.UUID, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 500 {
		return uuid.Nil, fmt.Errorf("%w: укажите причину коррекции (до 500 байт)", ErrInvalid)
	}
	f, err := s.store.Forecast(ctx, nil, id)
	if err != nil {
		return uuid.Nil, err
	}
	found := false
	for i := range f.Lines {
		if uuid.NewSHA1(id, []byte(strconv.Itoa(i))) == lineID {
			f.Lines[i].VolumeML = volume
			f.Lines[i].ClinicCode = clinic
			found = true
			break
		}
	}
	if !found {
		return uuid.Nil, ErrNotFound
	}
	f.ScenarioVersion = "adjusted-" + uuid.NewString()
	return s.acceptForecast(ctx, f, actor, "FORECAST_ADJUSTED", map[string]any{"original_forecast_id": id.String(), "original_line_id": lineID.String(), "adjust_reason": reason})
}
