// Package demand — контракт контекста «Территориальная потребность» (ФП2, FR016–FR020, FR045, FR046):
// приём файлов статистики, расчёт и публикация оценки потребности, доли «район → клиника».
// Топик demand.events; ключ — statistics_file_id, demand_estimate_id или clinic_share_version_id.
//
// В событиях о файлах нет содержимого строк: только число отклонённых строк. Построчные ошибки остаются
// в базе demand (file_row_error) и отдаются аналитику по HTTP.
package demand

import (
	"time"

	"github.com/google/uuid"
)

// Виды файлов статистики.
const (
	FileDistrictProfile    = "DISTRICT_PROFILE"
	FileConsumptionHistory = "CONSUMPTION_HISTORY"
	FileClinicShare        = "CLINIC_SHARE"
)

// FileKinds — все виды файлов в порядке схемы.
var FileKinds = []string{FileDistrictProfile, FileConsumptionHistory, FileClinicShare}

// Итог приёма файла: принят целиком или частично (часть строк отклонена).
const (
	FileAccepted = "ACCEPTED"
	FilePartial  = "PARTIAL"
)

// AcceptStatuses — итоги приёма в порядке схемы.
var AcceptStatuses = []string{FileAccepted, FilePartial}

// Причины отклонения файла целиком.
const (
	RejectDuplicateFile = "DUPLICATE_FILE"
	RejectInvalidFormat = "INVALID_FORMAT"
	RejectInvalidRows   = "INVALID_ROWS"
)

// FileRejectReasons — все причины отклонения в порядке схемы.
var FileRejectReasons = []string{RejectDuplicateFile, RejectInvalidFormat, RejectInvalidRows}

// StatisticsFileAccepted — файл статистики принят целиком или частично; rejected_rows — число отклонённых строк.
type StatisticsFileAccepted struct {
	StatisticsFileID uuid.UUID `json:"statistics_file_id"`
	FileKind         string    `json:"file_kind"`
	Status           string    `json:"status"`
	AcceptedRows     *int      `json:"accepted_rows,omitempty"`
	RejectedRows     int       `json:"rejected_rows"`
	UploadedBy       string    `json:"uploaded_by"`
}

// MessageType реализует contracts.Payload.
func (StatisticsFileAccepted) MessageType() string { return "StatisticsFileAccepted" }

// SchemaVersion реализует contracts.Payload.
func (StatisticsFileAccepted) SchemaVersion() int { return 1 }

// StatisticsFileRejected — файл статистики отклонён целиком. Только число ошибок, без содержимого строк.
type StatisticsFileRejected struct {
	StatisticsFileID uuid.UUID `json:"statistics_file_id"`
	FileKind         string    `json:"file_kind"`
	Reason           string    `json:"reason"`
	RejectedRows     int       `json:"rejected_rows"`
	UploadedBy       string    `json:"uploaded_by"`
}

// MessageType реализует contracts.Payload.
func (StatisticsFileRejected) MessageType() string { return "StatisticsFileRejected" }

// SchemaVersion реализует contracts.Payload.
func (StatisticsFileRejected) SchemaVersion() int { return 1 }

// DemandEstimateCalculated — рассчитана новая версия оценки потребности (статус CALCULATED, FR019).
type DemandEstimateCalculated struct {
	DemandEstimateID uuid.UUID `json:"demand_estimate_id"`
	VersionNo        int       `json:"version_no"`
	PeriodStart      time.Time `json:"period_start"`
	PeriodEnd        time.Time `json:"period_end"`
}

// MessageType реализует contracts.Payload.
func (DemandEstimateCalculated) MessageType() string { return "DemandEstimateCalculated" }

// SchemaVersion реализует contracts.Payload.
func (DemandEstimateCalculated) SchemaVersion() int { return 1 }

// DemandEstimatePublished — аналитик подтвердил публикацию версии оценки (PUBLISHED только с confirmed_by,
// FR020, NFR009). Строки — потребность клиники по виду и группе; группа пуста для видов без признака группы.
type DemandEstimatePublished struct {
	DemandEstimateID     uuid.UUID    `json:"demand_estimate_id"`
	VersionNo            int          `json:"version_no"`
	PeriodStart          time.Time    `json:"period_start"`
	PeriodEnd            time.Time    `json:"period_end"`
	ConfirmedBy          string       `json:"confirmed_by"`
	ClinicShareVersionID uuid.UUID    `json:"clinic_share_version_id"`
	Lines                []DemandLine `json:"lines"`
}

// DemandLine — потребность клиники по виду и группе за период.
type DemandLine struct {
	ClinicID       int64   `json:"clinic_id"`
	ComponentKind  string  `json:"component_kind"`
	BloodGroup     string  `json:"blood_group,omitempty"`
	DemandVolumeML float64 `json:"demand_volume_ml"`
}

// MessageType реализует contracts.Payload.
func (DemandEstimatePublished) MessageType() string { return "DemandEstimatePublished" }

// SchemaVersion реализует contracts.Payload.
func (DemandEstimatePublished) SchemaVersion() int { return 1 }

// ClinicSharesPublished — новая версия долей «район → клиника»; сумма долей по району равна 1 ± 0,0001.
type ClinicSharesPublished struct {
	ClinicShareVersionID uuid.UUID     `json:"clinic_share_version_id"`
	VersionNo            int           `json:"version_no"`
	Shares               []ClinicShare `json:"shares"`
}

// ClinicShare — доля клиники в потребности района.
type ClinicShare struct {
	DistrictID int64   `json:"district_id"`
	ClinicID   int64   `json:"clinic_id"`
	Share      float64 `json:"share"`
}

// MessageType реализует contracts.Payload.
func (ClinicSharesPublished) MessageType() string { return "ClinicSharesPublished" }

// SchemaVersion реализует contracts.Payload.
func (ClinicSharesPublished) SchemaVersion() int { return 1 }
