package inventory

// Контракты этапа 6: закрепление единиц за пациентом в АИСТ-М (ИНТ-05, FR051, FR052, NFR017). Команды
// AllocateUnitsToPatient и ReleasePatientAllocation пишет transfer в inventory.commands (ключ — obligation_id),
// квитанция PatientAllocationAcknowledged идёт в inventory.events. Повтор с тем же idempotency_key не создаёт
// второго закрепления в C01.
//
// Номер случая пациента передаётся только зашифрованным конвертом patient_case_sealed (ADR-0020): ключ есть
// у matching и inventory, transfer передаёт строку как есть и не может её прочитать.

import (
	"time"

	"github.com/google/uuid"
)

// Причины снятия закрепления (ReleasePatientAllocation).
const (
	ReleaseObligationCancelled = "OBLIGATION_CANCELLED"
	ReleaseObligationBroken    = "OBLIGATION_BROKEN"
	ReleaseUnitReplaced = "UNIT_REPLACED"
)

// ReleaseReasons — все причины снятия закрепления в порядке схемы.
var ReleaseReasons = []string{ReleaseObligationCancelled, ReleaseObligationBroken,ReleaseUnitReplaced}

// AllocateUnitsToPatient — команда закрепить подтверждённые единицы за пациентом в АИСТ-М (ИНТ-05, FR051).
type AllocateUnitsToPatient struct {
	ObligationID   uuid.UUID   `json:"obligation_id"`
	RequestID      uuid.UUID   `json:"request_id"`
	IdempotencyKey uuid.UUID   `json:"idempotency_key"`
	UnitIDs        []uuid.UUID `json:"unit_ids"`
	TargetClinicID int64       `json:"target_clinic_id"`
	// PatientCaseSealed — зашифрованный конверт номера случая (ADR-0020); не логировать.
	// Тип k1seal.Sealed после слияния stage6/wp1.
	PatientCaseSealed string `json:"patient_case_sealed"`
}

// MessageType реализует contracts.Payload.
func (AllocateUnitsToPatient) MessageType() string { return "AllocateUnitsToPatient" }

// SchemaVersion реализует contracts.Payload.
func (AllocateUnitsToPatient) SchemaVersion() int { return 1 }

// ReleasePatientAllocation — команда снять закрепление за пациентом при отмене или нарушении обязательства.
type ReleasePatientAllocation struct {
	ObligationID   uuid.UUID `json:"obligation_id"`
	IdempotencyKey uuid.UUID `json:"idempotency_key"`
	Reason         string    `json:"reason,omitempty"`
}

// MessageType реализует contracts.Payload.
func (ReleasePatientAllocation) MessageType() string { return "ReleasePatientAllocation" }

// SchemaVersion реализует contracts.Payload.
func (ReleasePatientAllocation) SchemaVersion() int { return 1 }

// PatientAllocationAcknowledged — АИСТ-М подтвердил приём закрепления (FR052): UI показывает «подтверждено АИСТ-М».
type PatientAllocationAcknowledged struct {
	ObligationID   uuid.UUID `json:"obligation_id"`
	IdempotencyKey uuid.UUID `json:"idempotency_key"`
	AcknowledgedAt time.Time `json:"acknowledged_at"`
}

// MessageType реализует contracts.Payload.
func (PatientAllocationAcknowledged) MessageType() string { return "PatientAllocationAcknowledged" }

// SchemaVersion реализует contracts.Payload.
func (PatientAllocationAcknowledged) SchemaVersion() int { return 1 }
