package inventory

// Контракты этапа 3 (ФП1): снимок запаса с водяным знаком, норматив, «запас ниже норматива», сеансы обмена
// с АИСТ-М (ИНТ-01) и выгрузка расхода для demand. Топик inventory.events; ключ — clinic_id:kind для снимков,
// нормативов и порога, sync_run_id для сеансов обмена.

import (
	"time"

	"github.com/google/uuid"
)

// Режимы сеанса обмена с АИСТ-М (sync_run.sync_kind, FR050).
const (
	SyncChanges       = "CHANGES"
	SyncDailySnapshot = "DAILY_SNAPSHOT"
	SyncOnDemand      = "ON_DEMAND"
	SyncUnitCheck     = "UNIT_CHECK"
)

// SyncKinds — все режимы в порядке схемы.
var SyncKinds = []string{SyncChanges, SyncDailySnapshot, SyncOnDemand, SyncUnitCheck}

// Итоги сеанса обмена (sync_run.sync_result).
const (
	SyncSuccess = "SUCCESS"
	SyncPartial = "PARTIAL"
	SyncFailed  = "FAILED"
)

// SyncResults — все итоги в порядке схемы.
var SyncResults = []string{SyncSuccess, SyncPartial, SyncFailed}

// StockSnapshotPublished — снимок запаса клиники по виду и группе после успешного сеанса обмена (O02, NFR015).
// Watermark — водяной знак: номер последнего успешного сеанса; проекции не применяют снимок старше сохранённого.
type StockSnapshotPublished struct {
	ClinicID      int64      `json:"clinic_id"`
	ComponentKind string     `json:"component_kind"`
	BloodGroup    string     `json:"blood_group"`
	UnitCount     int64      `json:"unit_count"`
	VolumeML      int64      `json:"volume_ml"`
	NearestExpiry *time.Time `json:"nearest_expiry,omitempty"`
	Watermark     int64      `json:"watermark"`
	SyncedAt      time.Time  `json:"synced_at"`
}

// MessageType реализует contracts.Payload.
func (StockSnapshotPublished) MessageType() string { return "StockSnapshotPublished" }

// SchemaVersion реализует contracts.Payload.
func (StockSnapshotPublished) SchemaVersion() int { return 1 }

// StockNormPublished — новый действующий норматив запаса клиники по виду (FR012, FR013). Объёмы — расчётные
// доли в мл с явным округлением, как numeric в ПР6.
type StockNormPublished struct {
	ClinicID         int64     `json:"clinic_id"`
	ComponentKind    string    `json:"component_kind"`
	NormML           float64   `json:"norm_ml"`
	AvgMonthlyML     float64   `json:"avg_monthly_ml"`
	IsMinUnitApplied *bool     `json:"is_min_unit_applied,omitempty"`
	Version          int64     `json:"version"`
	CalculatedAt     time.Time `json:"calculated_at"`
}

// MessageType реализует contracts.Payload.
func (StockNormPublished) MessageType() string { return "StockNormPublished" }

// SchemaVersion реализует contracts.Payload.
func (StockNormPublished) SchemaVersion() int { return 1 }

// StockBelowNorm — запас клиники по виду опустился ниже норматива (FR014). Публикуется только при переходе
// через порог сверху вниз, а не при каждом пересчёте: иначе уведомления (этап 7) шли бы потоком.
type StockBelowNorm struct {
	ClinicID      int64     `json:"clinic_id"`
	ComponentKind string    `json:"component_kind"`
	StockVolumeML int64     `json:"stock_volume_ml"`
	NormML        float64   `json:"norm_ml"`
	DetectedAt    time.Time `json:"detected_at"`
}

// MessageType реализует contracts.Payload.
func (StockBelowNorm) MessageType() string { return "StockBelowNorm" }

// SchemaVersion реализует contracts.Payload.
func (StockBelowNorm) SchemaVersion() int { return 1 }

// SyncRunCompleted — сеанс обмена с АИСТ-М (ИНТ-01) завершён. Расхождения — только число найденных и
// исправленных по суточному снимку; сами единицы в событие не попадают.
type SyncRunCompleted struct {
	SyncRunID          uuid.UUID  `json:"sync_run_id"`
	Kind               string     `json:"kind"`
	Result             string     `json:"result"`
	ClinicID           *int64     `json:"clinic_id,omitempty"`
	ChangesCount       int        `json:"changes_count"`
	DiscrepanciesCount *int       `json:"discrepancies_count,omitempty"`
	StartedAt          *time.Time `json:"started_at,omitempty"`
	FinishedAt         time.Time  `json:"finished_at"`
}

// MessageType реализует contracts.Payload.
func (SyncRunCompleted) MessageType() string { return "SyncRunCompleted" }

// SchemaVersion реализует contracts.Payload.
func (SyncRunCompleted) SchemaVersion() int { return 1 }

// ConsumptionExportPublished — выгрузка фактического расхода вида компонента за период для оценки потребности
// в demand (FR045). Без единиц и пациентов: только суммарный объём.
type ConsumptionExportPublished struct {
	ComponentKind    string    `json:"component_kind"`
	ClinicID         *int64    `json:"clinic_id,omitempty"`
	PeriodStart      time.Time `json:"period_start"`
	PeriodEnd        time.Time `json:"period_end"`
	ConsumedVolumeML int64     `json:"consumed_volume_ml"`
}

// MessageType реализует contracts.Payload.
func (ConsumptionExportPublished) MessageType() string { return "ConsumptionExportPublished" }

// SchemaVersion реализует contracts.Payload.
func (ConsumptionExportPublished) SchemaVersion() int { return 1 }
