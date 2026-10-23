// Package matching — контракт контекста «Заявка и подбор» (ФП4, FR029–FR035, FR043): заявка врача, варианты
// по правилам O15, выбор врача, дефицит заявки. Топик matching.events; ключ — clinical_request_id.
//
// Номера случая пациента в событиях нет ни в каком виде (NFR016, 152-ФЗ ст. 10): он живёт только в базе matching
// и уходит в ИНТ-05 зашифрованным (ADR-0020).
package matching

import "github.com/google/uuid"

// Причины дефицита заявки (RequestDeficitRecorded).
const (
	DeficitNoAllowedVariant   = "NO_ALLOWED_VARIANT"
	DeficitSourceRejected     = "SOURCE_REJECTED"
	DeficitNotConfirmedInTime = "NOT_CONFIRMED_IN_TIME"
	DeficitUnitWithdrawn      = "UNIT_WITHDRAWN"
)

// DeficitReasons — все причины дефицита заявки в порядке схемы.
var DeficitReasons = []string{DeficitNoAllowedVariant, DeficitSourceRejected, DeficitNotConfirmedInTime, DeficitUnitWithdrawn}

// Urgencies — коды срочности заявки (ref_urgency) в порядке схемы.
var Urgencies = []string{"PLANNED", "URGENT", "EMERGENCY"}

// ClinicalRequestCreated — врач создал заявку (O09). Без номера случая пациента.
type ClinicalRequestCreated struct {
	ClinicalRequestID uuid.UUID `json:"clinical_request_id"`
	ClinicID          int64     `json:"clinic_id"`
	ComponentKind     string    `json:"component_kind"`
	RequestedUnits    int       `json:"requested_units"`
	Urgency           string    `json:"urgency"`
	BloodGroup        string    `json:"blood_group"`
	CreatedBy         string    `json:"created_by"`
}

// MessageType реализует contracts.Payload.
func (ClinicalRequestCreated) MessageType() string { return "ClinicalRequestCreated" }

// SchemaVersion реализует contracts.Payload.
func (ClinicalRequestCreated) SchemaVersion() int { return 1 }

// VariantsOffered — ранжированные варианты для заявки (O10, FR030–FR034) на водяном знаке запаса.
type VariantsOffered struct {
	ClinicalRequestID uuid.UUID      `json:"clinical_request_id"`
	Variants          []VariantOffer `json:"variants"`
	StockWatermark    int64          `json:"stock_watermark"`
}

// VariantOffer — допустимый вариант: источник, вид и группа, доступно, время получения, правило O15.
type VariantOffer struct {
	VariantOfferID     uuid.UUID `json:"variant_offer_id"`
	RankNo             int       `json:"rank_no"`
	SourceClinicID     int64     `json:"source_clinic_id"`
	ComponentKind      string    `json:"component_kind"`
	BloodGroup         string    `json:"blood_group"`
	AvailableUnits     int       `json:"available_units"`
	DeliveryMinutes    int       `json:"delivery_minutes"`
	IsDeficitStockUsed bool      `json:"is_deficit_stock_used"`
	MatchingRuleID     uuid.UUID `json:"matching_rule_id"`
}

// MessageType реализует contracts.Payload.
func (VariantsOffered) MessageType() string { return "VariantsOffered" }

// SchemaVersion реализует contracts.Payload.
func (VariantsOffered) SchemaVersion() int { return 1 }

// DoctorChoiceRecorded — врач выбрал вариант (FR043). Местный вариант закрепления в АС не создаёт.
type DoctorChoiceRecorded struct {
	ClinicalRequestID uuid.UUID `json:"clinical_request_id"`
	VariantOfferID    uuid.UUID `json:"variant_offer_id"`
	SelectedBy        string    `json:"selected_by"`
	IsLocal           bool      `json:"is_local"`
}

// MessageType реализует contracts.Payload.
func (DoctorChoiceRecorded) MessageType() string { return "DoctorChoiceRecorded" }

// SchemaVersion реализует contracts.Payload.
func (DoctorChoiceRecorded) SchemaVersion() int { return 1 }

// RequestDeficitRecorded — заявка не покрыта; planning ведёт её в реестре дефицита (O11).
type RequestDeficitRecorded struct {
	ClinicalRequestID uuid.UUID `json:"clinical_request_id"`
	ClinicID          int64     `json:"clinic_id"`
	ComponentKind     string    `json:"component_kind"`
	BloodGroup        string    `json:"blood_group"`
	UncoveredUnits    int       `json:"uncovered_units"`
	Reason            string    `json:"reason"`
}

// MessageType реализует contracts.Payload.
func (RequestDeficitRecorded) MessageType() string { return "RequestDeficitRecorded" }

// SchemaVersion реализует contracts.Payload.
func (RequestDeficitRecorded) SchemaVersion() int { return 1 }

// ClinicalRequestCancelled — врач отменил заявку; задания по ней отменяются командой CancelObligation.
type ClinicalRequestCancelled struct {
	ClinicalRequestID uuid.UUID `json:"clinical_request_id"`
	CancelledBy       string    `json:"cancelled_by"`
}

// MessageType реализует contracts.Payload.
func (ClinicalRequestCancelled) MessageType() string { return "ClinicalRequestCancelled" }

// SchemaVersion реализует contracts.Payload.
func (ClinicalRequestCancelled) SchemaVersion() int { return 1 }
