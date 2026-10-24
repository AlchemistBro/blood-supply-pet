// Package transfer — контракт контекста «Передачи и обязательства» (O07, FR048, FR049, NFR004).
//
// Команды OpenObligation и CancelObligation идут в топик transfer.commands (пишут matching и planning,
// исполняет transfer), события — в transfer.events. Ключ сообщения — obligation_id (для OpenObligation —
// choice_id). Номера случая пациента здесь нет: обязательство ссылается на заявку только через request_id,
// ИНТ-05 (закрепление за пациентом) — забота inventory. Спецификация замены содержит фенотип —
// квазиидентификатор (ADR-0011): её не логируют и не пересылают дальше OpenObligation.
package transfer

import (
	"time"

	"github.com/google/uuid"
)

// Origin — откуда пришло обязательство.
const (
	OriginDoctorChoice = "DOCTOR_CHOICE"
	OriginPlanProposal = "PLAN_PROPOSAL"
)

// Origins — все источники обязательства в порядке схемы.
var Origins = []string{OriginDoctorChoice, OriginPlanProposal}

// Срочность — коды справочника ref_urgency (libs/refdata).
const (
	UrgencyPlanned   = "PLANNED"
	UrgencyUrgent    = "URGENT"
	UrgencyEmergency = "EMERGENCY"
)

// Urgencies — все коды срочности в порядке схемы.
var Urgencies = []string{UrgencyPlanned, UrgencyUrgent, UrgencyEmergency}

// Причины отмены обязательства (CancelObligation, TransferCancelled).
const (
	CancelRequestCancelled    = "REQUEST_CANCELLED"
	CancelPlanRecalculated    = "PLAN_RECALCULATED"
	CancelDutyOfficerDecision = "DUTY_OFFICER_DECISION"
)

// CancelReasons — все причины отмены в порядке схемы.
var CancelReasons = []string{CancelRequestCancelled, CancelPlanRecalculated, CancelDutyOfficerDecision}

// Причины отклонения (TransferRejected): все выбранные единицы выбыли или источник отказал.
const (
	RejectUnitsWithdrawn = "UNITS_WITHDRAWN"
	RejectSourceRefused  = "SOURCE_REFUSED"
)

// RejectReasons — все причины отклонения в порядке схемы.
var RejectReasons = []string{RejectUnitsWithdrawn, RejectSourceRefused}

// Причины нарушения обязательства (TransferBroken). Первые четыре повторяют причины выбытия единицы
// (inventory.UnitWithdrawn), QUARANTINE — закреплённая единица получила статус «на карантине» (ПП РФ № 641,
// п. 62 а): она не выбыла, но обязательство нарушается — решение проекта (ADR-0027). Поэтому перечисление
// своё, а не общее с UnitWithdrawn.
const (
	BrokenIssued      = "ISSUED"
	BrokenDiscarded   = "DISCARDED"
	BrokenTransferred = "TRANSFERRED"
	BrokenExpired     = "EXPIRED"
	BrokenQuarantine  = "QUARANTINE"
)

// BrokenReasons — все причины нарушения в порядке схемы.
var BrokenReasons = []string{BrokenIssued, BrokenDiscarded, BrokenTransferred, BrokenExpired, BrokenQuarantine}

// Причины эскалации дежурному (EscalationRaised).
const (
	EscalationExpiredNoFallback       = "EXPIRED_NO_FALLBACK"
	EscalationTransportRefused        = "TRANSPORT_REFUSED"
	EscalationTransportOutcomeUnknown = "TRANSPORT_OUTCOME_UNKNOWN"
	EscalationSourceSystemUnavailable = "SOURCE_SYSTEM_UNAVAILABLE"
	// EscalationTemperatureExcursion — нарушены условия перевозки (ПП РФ № 641, п. 69, 98; ADR-0022), этап 7.
	EscalationTemperatureExcursion = "TEMPERATURE_EXCURSION"
)

// EscalationReasons — все причины эскалации в порядке схемы.
var EscalationReasons = []string{EscalationExpiredNoFallback, EscalationTransportRefused,
	EscalationTransportOutcomeUnknown, EscalationSourceSystemUnavailable, EscalationTemperatureExcursion}

// ReplacementSpec — спецификация допустимой замены единицы (ADR-0011). Её считает matching
// (или planning) по правилам O15, transfer только проверяет кандидата. Пустое поле — «любой».
type ReplacementSpec struct {
	BloodGroup *string `json:"blood_group,omitempty"`
	RhPositive *bool   `json:"rh_positive,omitempty"`
	// Phenotype — квазиидентификатор: не логируется, не попадает в события и письма.
	Phenotype    string     `json:"phenotype,omitempty"`
	MinExpiresAt *time.Time `json:"min_expires_at,omitempty"`
	// RuleVersion — версия правила O15, по которой посчитана спецификация (NFR009).
	RuleVersion string `json:"rule_version"`
}

// OpenObligation — команда «создать задание источнику» (O07 PENDING). Повтор с тем же choice_id
// не создаёт второе обязательство.
type OpenObligation struct {
	ChoiceID       uuid.UUID `json:"choice_id"`
	RequestID      uuid.UUID `json:"request_id"`
	Origin         string    `json:"origin"`
	SourceClinicID int64     `json:"source_clinic_id"`
	TargetClinicID int64     `json:"target_clinic_id"`
	ComponentKind  string    `json:"component_kind"`
	RequestedQty   int       `json:"requested_qty"`
	Urgency        string    `json:"urgency"`
	// ConfirmWindowSeconds — срок подтверждения оператором источника (FR049); для экстренной — минуты.
	ConfirmWindowSeconds int64 `json:"confirm_window_seconds"`
	// FallbackSources — следующие источники для эскалации по таймеру, по порядку ранжирования.
	FallbackSources []int64         `json:"fallback_sources,omitempty"`
	Spec            ReplacementSpec `json:"spec"`
	// PatientCaseSealed — номер случая пациента в зашифрованном конверте v1.<kid>.<base64url> (ADR-0020, этап 6).
	// Только для выбора врача; transfer не может его прочитать, хранит как есть и передаёт в AllocateUnitsToPatient.
	// В события transfer не попадает, не логируется. Тип k1seal.Sealed после слияния stage6/wp1.
	PatientCaseSealed string `json:"patient_case_sealed,omitempty"`
}

// MessageType реализует contracts.Payload.
func (OpenObligation) MessageType() string { return "OpenObligation" }

// SchemaVersion реализует contracts.Payload.
func (OpenObligation) SchemaVersion() int { return 1 }

// CancelObligation — команда «отменить обязательство»: заявка отменена или план пересчитан.
type CancelObligation struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	Reason       string    `json:"reason"`
}

// MessageType реализует contracts.Payload.
func (CancelObligation) MessageType() string { return "CancelObligation" }

// SchemaVersion реализует contracts.Payload.
func (CancelObligation) SchemaVersion() int { return 1 }

// ObligationOpened — обязательство создано в статусе PENDING.
type ObligationOpened struct {
	// Urgency — необязательна для совместимости с ранними событиями; новые события передают срочность явно.
	Urgency      string    `json:"urgency,omitempty"`
	ObligationID uuid.UUID `json:"obligation_id"`
	// ObligationVersion — версия O07 после перехода: проекции применяют событие, только если версия выше сохранённой.
	ObligationVersion *int64    `json:"obligation_version,omitempty"`
	RequestID         uuid.UUID `json:"request_id"`
	Origin            string    `json:"origin"`
	SourceClinicID    int64     `json:"source_clinic_id"`
	TargetClinicID    int64     `json:"target_clinic_id"`
	ComponentKind     string    `json:"component_kind"`
	RequestedQty      int       `json:"requested_qty"`
	Deadline          time.Time `json:"deadline"`
}

// MessageType реализует contracts.Payload.
func (ObligationOpened) MessageType() string { return "ObligationOpened" }

// SchemaVersion реализует contracts.Payload.
func (ObligationOpened) SchemaVersion() int { return 1 }

// TransferConfirmed — оператор источника подтвердил передачу, единицы закреплены (PENDING → CONFIRMED).
type TransferConfirmed struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	// ObligationVersion — версия O07 после перехода: проекции применяют событие, только если версия выше сохранённой.
	ObligationVersion *int64 `json:"obligation_version,omitempty"`
	// SourceClinicID и TargetClinicID — клиники обязательства, необязательны: по ним web фильтрует живые статусы,
	// не держа индекс «обязательство → клиники» в памяти.
	SourceClinicID *int64      `json:"source_clinic_id,omitempty"`
	TargetClinicID *int64      `json:"target_clinic_id,omitempty"`
	RequestID      uuid.UUID   `json:"request_id"`
	Units          []uuid.UUID `json:"units"`
	Quantity       int         `json:"quantity"`
	// Operator — учётная запись оператора из токена (как user_id в аудите), не ФИО.
	Operator string `json:"operator"`
}

// MessageType реализует contracts.Payload.
func (TransferConfirmed) MessageType() string { return "TransferConfirmed" }

// SchemaVersion реализует contracts.Payload.
func (TransferConfirmed) SchemaVersion() int { return 1 }

// TransferShortfallRecorded — подтверждено меньше запрошенного; подбор или план ищет источник на остаток.
type TransferShortfallRecorded struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	// ObligationVersion — версия O07 после перехода: проекции применяют событие, только если версия выше сохранённой.
	ObligationVersion *int64 `json:"obligation_version,omitempty"`
	// SourceClinicID и TargetClinicID — клиники обязательства, необязательны: по ним web фильтрует живые статусы,
	// не держа индекс «обязательство → клиники» в памяти.
	SourceClinicID *int64    `json:"source_clinic_id,omitempty"`
	TargetClinicID *int64    `json:"target_clinic_id,omitempty"`
	RequestID      uuid.UUID `json:"request_id"`
	ComponentKind  string    `json:"component_kind"`
	Shortfall      int       `json:"shortfall"`
}

// MessageType реализует contracts.Payload.
func (TransferShortfallRecorded) MessageType() string { return "TransferShortfallRecorded" }

// SchemaVersion реализует contracts.Payload.
func (TransferShortfallRecorded) SchemaVersion() int { return 1 }

// TransferRejected — обязательство отклонено (PENDING → REJECTED).
type TransferRejected struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	// ObligationVersion — версия O07 после перехода: проекции применяют событие, только если версия выше сохранённой.
	ObligationVersion *int64 `json:"obligation_version,omitempty"`
	// SourceClinicID и TargetClinicID — клиники обязательства, необязательны: по ним web фильтрует живые статусы,
	// не держа индекс «обязательство → клиники» в памяти.
	SourceClinicID *int64    `json:"source_clinic_id,omitempty"`
	TargetClinicID *int64    `json:"target_clinic_id,omitempty"`
	RequestID      uuid.UUID `json:"request_id"`
	Reason         string    `json:"reason"`
}

// MessageType реализует contracts.Payload.
func (TransferRejected) MessageType() string { return "TransferRejected" }

// SchemaVersion реализует contracts.Payload.
func (TransferRejected) SchemaVersion() int { return 1 }

// TransferExpired — срок подтверждения истёк (PENDING → EXPIRED). NextObligationID — новое обязательство
// у следующего источника, если он был в fallback_sources.
type TransferExpired struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	// ObligationVersion — версия O07 после перехода: проекции применяют событие, только если версия выше сохранённой.
	ObligationVersion *int64 `json:"obligation_version,omitempty"`
	// SourceClinicID и TargetClinicID — клиники обязательства, необязательны: по ним web фильтрует живые статусы,
	// не держа индекс «обязательство → клиники» в памяти.
	SourceClinicID   *int64     `json:"source_clinic_id,omitempty"`
	TargetClinicID   *int64     `json:"target_clinic_id,omitempty"`
	RequestID        uuid.UUID  `json:"request_id"`
	NextObligationID *uuid.UUID `json:"next_obligation_id,omitempty"`
}

// MessageType реализует contracts.Payload.
func (TransferExpired) MessageType() string { return "TransferExpired" }

// SchemaVersion реализует contracts.Payload.
func (TransferExpired) SchemaVersion() int { return 1 }

// TransferCancelled — обязательство отменено, закрепления сняты.
type TransferCancelled struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	// ObligationVersion — версия O07 после перехода: проекции применяют событие, только если версия выше сохранённой.
	ObligationVersion *int64 `json:"obligation_version,omitempty"`
	// SourceClinicID и TargetClinicID — клиники обязательства, необязательны: по ним web фильтрует живые статусы,
	// не держа индекс «обязательство → клиники» в памяти.
	SourceClinicID *int64    `json:"source_clinic_id,omitempty"`
	TargetClinicID *int64    `json:"target_clinic_id,omitempty"`
	RequestID      uuid.UUID `json:"request_id"`
	Reason         string    `json:"reason"`
}

// MessageType реализует contracts.Payload.
func (TransferCancelled) MessageType() string { return "TransferCancelled" }

// SchemaVersion реализует contracts.Payload.
func (TransferCancelled) SchemaVersion() int { return 1 }

// TransferBroken — закреплённая единица выбыла в АИСТ-М или ушла на карантин, обязательство нарушено (→ BROKEN).
// Reason — одна из BrokenReasons.
type TransferBroken struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	// ObligationVersion — версия O07 после перехода: проекции применяют событие, только если версия выше сохранённой.
	ObligationVersion *int64 `json:"obligation_version,omitempty"`
	// SourceClinicID и TargetClinicID — клиники обязательства, необязательны: по ним web фильтрует живые статусы,
	// не держа индекс «обязательство → клиники» в памяти.
	SourceClinicID *int64    `json:"source_clinic_id,omitempty"`
	TargetClinicID *int64    `json:"target_clinic_id,omitempty"`
	RequestID      uuid.UUID `json:"request_id"`
	UnitID         uuid.UUID `json:"unit_id"`
	Reason         string    `json:"reason"`
}

// MessageType реализует contracts.Payload.
func (TransferBroken) MessageType() string { return "TransferBroken" }

// SchemaVersion реализует contracts.Payload.
func (TransferBroken) SchemaVersion() int { return 1 }

// TransferUnitReplaced — закреплённая единица заменена равноценной до отправки (ADR-0011).
type TransferUnitReplaced struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	// ObligationVersion — версия O07 после перехода: проекции применяют событие, только если версия выше сохранённой.
	ObligationVersion *int64 `json:"obligation_version,omitempty"`
	// SourceClinicID и TargetClinicID — клиники обязательства, необязательны: по ним web фильтрует живые статусы,
	// не держа индекс «обязательство → клиники» в памяти.
	SourceClinicID *int64    `json:"source_clinic_id,omitempty"`
	TargetClinicID *int64    `json:"target_clinic_id,omitempty"`
	OldUnitID      uuid.UUID `json:"old_unit_id"`
	NewUnitID      uuid.UUID `json:"new_unit_id"`
	RuleVersion    string    `json:"rule_version"`
}

// MessageType реализует contracts.Payload.
func (TransferUnitReplaced) MessageType() string { return "TransferUnitReplaced" }

// SchemaVersion реализует contracts.Payload.
func (TransferUnitReplaced) SchemaVersion() int { return 1 }

// TransportRequested — перевозчик принял заказ (CONFIRMED → TRANSPORT_REQUESTED).
type TransportRequested struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	// ObligationVersion — версия O07 после перехода: проекции применяют событие, только если версия выше сохранённой.
	ObligationVersion *int64 `json:"obligation_version,omitempty"`
	// SourceClinicID и TargetClinicID — клиники обязательства, необязательны: по ним web фильтрует живые статусы,
	// не держа индекс «обязательство → клиники» в памяти.
	SourceClinicID *int64 `json:"source_clinic_id,omitempty"`
	TargetClinicID *int64 `json:"target_clinic_id,omitempty"`
	ShipmentRef    string `json:"shipment_ref"`
}

// MessageType реализует contracts.Payload.
func (TransportRequested) MessageType() string { return "TransportRequested" }

// SchemaVersion реализует contracts.Payload.
func (TransportRequested) SchemaVersion() int { return 1 }

// TransportRequestFailed — заказ перевозки не состоялся; O07 остаётся CONFIRMED, перевозку организует дежурный.
// Reason — причина из logistics.TransportRejected.
type TransportRequestFailed struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	// ObligationVersion — версия O07 после перехода: проекции применяют событие, только если версия выше сохранённой.
	ObligationVersion *int64 `json:"obligation_version,omitempty"`
	// SourceClinicID и TargetClinicID — клиники обязательства, необязательны: по ним web фильтрует живые статусы,
	// не держа индекс «обязательство → клиники» в памяти.
	SourceClinicID *int64 `json:"source_clinic_id,omitempty"`
	TargetClinicID *int64 `json:"target_clinic_id,omitempty"`
	Reason         string `json:"reason"`
	Details        string `json:"details,omitempty"`
}

// MessageType реализует contracts.Payload.
func (TransportRequestFailed) MessageType() string { return "TransportRequestFailed" }

// SchemaVersion реализует contracts.Payload.
func (TransportRequestFailed) SchemaVersion() int { return 1 }

// EscalationRaised — ситуация передана дежурному смены: таймер без запасного источника, отказ или
// неизвестный исход перевозки, недоступность АИСТ-М при экстренной заявке.
type EscalationRaised struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	// ObligationVersion — версия O07 после перехода: проекции применяют событие, только если версия выше сохранённой.
	ObligationVersion *int64 `json:"obligation_version,omitempty"`
	// SourceClinicID и TargetClinicID — клиники обязательства, необязательны: по ним web фильтрует живые статусы,
	// не держа индекс «обязательство → клиники» в памяти.
	SourceClinicID *int64    `json:"source_clinic_id,omitempty"`
	TargetClinicID *int64    `json:"target_clinic_id,omitempty"`
	RequestID      uuid.UUID `json:"request_id"`
	Reason         string    `json:"reason"`
	At             time.Time `json:"at"`
}

// MessageType реализует contracts.Payload.
func (EscalationRaised) MessageType() string { return "EscalationRaised" }

// SchemaVersion реализует contracts.Payload.
func (EscalationRaised) SchemaVersion() int { return 1 }
