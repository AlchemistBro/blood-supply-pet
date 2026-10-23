// Package planning — контракт контекста «Планирование пополнения» (ФП3, FR021–FR028, FR035): приём прогноза ЧС,
// план пополнения, предложения передач, дефицит плана и защищённый объём источника. Топик planning.events;
// ключ — replenishment_plan_id, emergency_forecast_id, deficit_record_id или clinic_id:kind для защищённого объёма.
package planning

import (
	"time"

	"github.com/google/uuid"
)

// Причины дефицита плана (deficit_record.deficit_reason для deficit_kind = PLAN).
const (
	DeficitNoSource           = "NO_SOURCE"
	DeficitSourceRejected     = "SOURCE_REJECTED"
	DeficitNotConfirmedInTime = "NOT_CONFIRMED_IN_TIME"
	DeficitUnitWithdrawn      = "UNIT_WITHDRAWN"
)

// DeficitReasons — все причины дефицита плана в порядке схемы.
var DeficitReasons = []string{DeficitNoSource, DeficitSourceRejected, DeficitNotConfirmedInTime, DeficitUnitWithdrawn}

// UnitsForVolume пересчитывает защищённый объём из мл в единицы (ADR-0019): округление вверх по типовому
// объёму единицы вида (ref_component_kind.typical_volume_ml), чтобы защищённый запас не оказался меньше
// расчётного. Остаток transfer считает единицы, поэтому в ProtectedVolumeCalculated идут единицы.
func UnitsForVolume(volumeML, typicalVolumeML int64) int64 {
	if volumeML <= 0 || typicalVolumeML <= 0 {
		return 0
	}
	return (volumeML + typicalVolumeML - 1) / typicalVolumeML
}

// EmergencyForecastAccepted — прогноз ЧС (ИНТ-02) принят после фильтра белого списка (NFR010).
// IsRoughEstimate — принимающих клиник в прогнозе не было, распределение по долям ФП2 (FR024).
type EmergencyForecastAccepted struct {
	EmergencyForecastID uuid.UUID `json:"emergency_forecast_id"`
	ScenarioVersion     string    `json:"scenario_version"`
	Territory           string    `json:"territory"`
	PeriodStart         time.Time `json:"period_start"`
	PeriodEnd           time.Time `json:"period_end"`
	IsRoughEstimate     bool      `json:"is_rough_estimate"`
	LinesCount          int       `json:"lines_count"`
}

// MessageType реализует contracts.Payload.
func (EmergencyForecastAccepted) MessageType() string { return "EmergencyForecastAccepted" }

// SchemaVersion реализует contracts.Payload.
func (EmergencyForecastAccepted) SchemaVersion() int { return 1 }

// ReplenishmentPlanBuilt — построен план пополнения (O06). Версии входов — для воспроизводимости решения (NFR009):
// версия оценки потребности, прогноз ЧС, водяной знак запаса, время оценок доставки. ElapsedMS — NFR002.
type ReplenishmentPlanBuilt struct {
	ReplenishmentPlanID     uuid.UUID  `json:"replenishment_plan_id"`
	DemandEstimateID        uuid.UUID  `json:"demand_estimate_id"`
	DemandEstimateVersionNo int        `json:"demand_estimate_version_no"`
	EmergencyForecastID     *uuid.UUID `json:"emergency_forecast_id,omitempty"`
	ScenarioVersion         string     `json:"scenario_version,omitempty"`
	StockWatermark          int64      `json:"stock_watermark"`
	StockSyncedAt           *time.Time `json:"stock_synced_at,omitempty"`
	DeliveryEstimatedAt     *time.Time `json:"delivery_estimated_at,omitempty"`
	CoverageRatio           float64    `json:"coverage_ratio"`
	TargetsCount            int        `json:"targets_count"`
	ProposalsCount          int        `json:"proposals_count"`
	DeficitsCount           *int       `json:"deficits_count,omitempty"`
	ElapsedMS               int64      `json:"elapsed_ms"`
}

// MessageType реализует contracts.Payload.
func (ReplenishmentPlanBuilt) MessageType() string { return "ReplenishmentPlanBuilt" }

// SchemaVersion реализует contracts.Payload.
func (ReplenishmentPlanBuilt) SchemaVersion() int { return 1 }

// TransferProposed — предложение передачи из плана; proposal_id становится choice_id команды OpenObligation.
type TransferProposed struct {
	ProposalID          uuid.UUID `json:"proposal_id"`
	ReplenishmentPlanID uuid.UUID `json:"replenishment_plan_id"`
	SourceClinicID      int64     `json:"source_clinic_id"`
	TargetClinicID      int64     `json:"target_clinic_id"`
	ComponentKind       string    `json:"component_kind"`
	BloodGroup          string    `json:"blood_group"`
	Units               int       `json:"units"`
	DeliveryMinutes     int       `json:"delivery_minutes"`
	FallbackSources     []int64   `json:"fallback_sources,omitempty"`
}

// MessageType реализует contracts.Payload.
func (TransferProposed) MessageType() string { return "TransferProposed" }

// SchemaVersion реализует contracts.Payload.
func (TransferProposed) SchemaVersion() int { return 1 }

// PlanDeficitRecorded — непокрытая часть целевого запаса записана в реестр дефицита (O11, NFR014: всегда).
type PlanDeficitRecorded struct {
	DeficitRecordID     uuid.UUID `json:"deficit_record_id"`
	ReplenishmentPlanID uuid.UUID `json:"replenishment_plan_id"`
	ClinicID            int64     `json:"clinic_id"`
	ComponentKind       string    `json:"component_kind"`
	BloodGroup          string    `json:"blood_group,omitempty"`
	UncoveredVolumeML   float64   `json:"uncovered_volume_ml"`
	Reason              string    `json:"reason"`
}

// MessageType реализует contracts.Payload.
func (PlanDeficitRecorded) MessageType() string { return "PlanDeficitRecorded" }

// SchemaVersion реализует contracts.Payload.
func (PlanDeficitRecorded) SchemaVersion() int { return 1 }

// ProtectedVolumeCalculated — защищённый объём источника по виду, посчитанный планированием (FR025, NFR013):
// норматив + фоновая потребность за период + подтверждённые обязательства. VolumeUnits — в единицах, не в мл:
// пересчёт UnitsForVolume (округление вверх по типовому объёму вида), ADR-0019. inventory как владелец
// запаса публикует его дальше событием ProtectedVolumePublished.
type ProtectedVolumeCalculated struct {
	ClinicID            int64      `json:"clinic_id"`
	ComponentKind       string     `json:"component_kind"`
	VolumeUnits         int64      `json:"volume_units"`
	VolumeML            *float64   `json:"volume_ml,omitempty"`
	Version             int64      `json:"version"`
	Reason              string     `json:"reason"`
	ReplenishmentPlanID *uuid.UUID `json:"replenishment_plan_id,omitempty"`
	CalculatedAt        time.Time  `json:"calculated_at"`
}

// MessageType реализует contracts.Payload.
func (ProtectedVolumeCalculated) MessageType() string { return "ProtectedVolumeCalculated" }

// SchemaVersion реализует contracts.Payload.
func (ProtectedVolumeCalculated) SchemaVersion() int { return 1 }
