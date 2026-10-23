package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
)

// Origin — откуда пришло задание.
type Origin string

// Происхождение задания.
const (
	OriginDoctorChoice Origin = transfer.OriginDoctorChoice
	OriginPlanProposal Origin = transfer.OriginPlanProposal
)

// UrgencyEmergency — код экстренной срочности (ref_urgency).
const UrgencyEmergency = transfer.UrgencyEmergency

// EscalationReason — повод для EscalationRaised: дежурный смены должен вмешаться.
type EscalationReason string

// Поводы эскалации.
const (
	EscalationExpiredNoFallback       EscalationReason = transfer.EscalationExpiredNoFallback
	EscalationTransportRefused        EscalationReason = transfer.EscalationTransportRefused
	EscalationTransportOutcomeUnknown EscalationReason = transfer.EscalationTransportOutcomeUnknown
	EscalationSourceSystemUnavailable EscalationReason = transfer.EscalationSourceSystemUnavailable
)

// Obligation — обязательство на передачу O07 в том виде, в котором оно хранится в базе transfer.
// Номера случая пациента здесь нет: заявку представляет только request_id.
type Obligation struct {
	ID              uuid.UUID
	ChoiceID        uuid.UUID
	RequestID       uuid.UUID
	Origin          Origin
	SourceClinicID  int64
	TargetClinicID  int64
	ComponentKind   string
	BloodGroup      *string
	RequestedQty    int
	ConfirmedQty    int
	Status          Status
	UrgencyCode     string
	Deadline        time.Time
	ConfirmWindow   time.Duration
	FallbackSources []int64
	Spec            ReplacementSpec
	ConfirmKey      string
	TransportRef    string
	StatusReason    string
	Version         int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// NewObligation — данные для открытия задания (команда OpenObligation или эскалация по таймеру).
type NewObligation struct {
	ChoiceID        uuid.UUID
	RequestID       uuid.UUID
	Origin          Origin
	SourceClinicID  int64
	TargetClinicID  int64
	ComponentKind   string
	RequestedQty    int
	UrgencyCode     string
	ConfirmWindow   time.Duration // ноль — взять из справочника срочности
	FallbackSources []int64
	Spec            ReplacementSpec
}

// ErrInvalidObligation — команда открытия некорректна; повтор её не исправит.
var ErrInvalidObligation = errors.New("некорректная команда открытия обязательства")

// Validate проверяет инварианты, которые дублируют CHECK в схеме: ошибка понятнее нарушения ограничения.
func (n NewObligation) Validate() error {
	var problem string
	switch {
	case n.ChoiceID == uuid.Nil:
		problem = "нет choice_id"
	case n.RequestID == uuid.Nil:
		problem = "нет request_id"
	case n.Origin != OriginDoctorChoice && n.Origin != OriginPlanProposal:
		problem = fmt.Sprintf("неизвестное происхождение %q", n.Origin)
	case n.SourceClinicID == n.TargetClinicID:
		problem = "источник совпадает с получателем"
	case n.ComponentKind == "":
		problem = "не указан вид компонента"
	case n.RequestedQty <= 0:
		problem = "requested_qty должно быть больше нуля"
	case n.ConfirmWindow < 0:
		problem = "отрицательный срок подтверждения"
	case n.Spec.RuleVersion == "":
		problem = "нет версии правила спецификации замены"
	}
	if problem != "" {
		return fmt.Errorf("%w: %s", ErrInvalidObligation, problem)
	}
	return nil
}

// Allocation — закрепление единицы O18. Строки не удаляются: по ним видно, куда уходила единица (ПП РФ № 641).
type Allocation struct {
	ID           uuid.UUID
	UnitID       uuid.UUID
	ObligationID uuid.UUID
	Status       AllocationStatus
	CreatedAt    time.Time
	ClosedAt     *time.Time
}

// AllocationStatus — статус закрепления.
type AllocationStatus string

// Статусы закрепления.
const (
	AllocationConfirmed AllocationStatus = "CONFIRMED"
	AllocationReleased  AllocationStatus = "RELEASED"
	AllocationConsumed  AllocationStatus = "CONSUMED"
)
