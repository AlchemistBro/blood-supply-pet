// Package domain — правила сервиса уведомлений: какое событие порождает письмо, кому оно адресовано (роль и клиника,
// ADR-0021), какие сведения в нём допустимы и как повторяется отправка. Без зависимостей от БД, Kafka и HTTP.
//
// Сведения письма (Facts) — закрытый набор полей: номер задания, клиники, вид компонента, количество, срочность,
// срок, причина. Номера случая пациента, фенотипа и спецификации замены, ФИО здесь нет и взяться им неоткуда
// (FR044, NFR016, ADR-0011).
package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
)

// Kind — вид уведомления.
type Kind string

// Виды уведомлений.
const (
	KindTransferTerminal Kind = "TRANSFER_TERMINAL"
	KindStockBelowNorm   Kind = "STOCK_BELOW_NORM"
	KindSecurityIncident Kind = "SECURITY_INCIDENT"
	// KindUrgentTask — срочное задание на передачу по выбору врача, оператору клиники-источника (FR044).
	KindUrgentTask Kind = "URGENT_TASK"
	// KindEscalation — эскалация дежурному смены (EscalationRaised, ADR-0027).
	KindEscalation Kind = "ESCALATION"
	// KindTransferBroken — обязательство на передачу нарушено, дежурному смены.
	KindTransferBroken Kind = "TRANSFER_BROKEN"
)

// Status — статус доставки письма.
type Status string

// Статусы доставки.
const (
	StatusPending Status = "PENDING"
	StatusSent    Status = "SENT"
	StatusFailed  Status = "FAILED"
)

// Роли адресатов — роли realm (docs/architecture/security.md). Письмо уходит ящику подразделения, а не человеку.
const (
	RoleOperator  = "CLINIC_OPERATOR"
	RoleDuty      = "DUTY_OFFICER"
	RolePhysician = "PHYSICIAN"
	RoleIT        = "IT_SERVICE"
)

// Происхождение и срочность задания — коды контракта transfer.
const (
	OriginDoctorChoice = transfer.OriginDoctorChoice
	OriginPlanProposal = transfer.OriginPlanProposal
	UrgencyPlanned     = transfer.UrgencyPlanned
	UrgencyUrgent      = transfer.UrgencyUrgent
	UrgencyEmergency   = transfer.UrgencyEmergency
)

// Recipient — адресат: роль и клиника (для оператора). Адрес ящика даёт справочник адресатов.
type Recipient struct {
	Role     string
	ClinicID *int64
}

// Obligation — сведения о задании из ObligationOpened (проекция).
type Obligation struct {
	ObligationID   uuid.UUID
	RequestID      uuid.UUID
	Origin         string
	SourceClinicID int64
	TargetClinicID int64
	ComponentKind  string
	RequestedQty   int
	// Urgency — срочность; в ObligationOpened v1 её нет, пусто — неизвестна.
	Urgency  string
	Deadline time.Time
}

// Facts — всё, что может попасть в письмо. Пустое поле в письмо не выводится.
type Facts struct {
	ObligationID   uuid.UUID
	RequestID      uuid.UUID
	SourceClinicID *int64
	TargetClinicID *int64
	ComponentKind  string
	Quantity       *int
	Urgency        string
	Deadline       *time.Time
	// Reason — код причины эскалации или нарушения.
	Reason string
}

// Draft — решение «отправить письмо»: вид, адресат, сведения.
type Draft struct {
	Kind  Kind
	To    Recipient
	Facts Facts
}

// Notification — запись журнала писем.
type Notification struct {
	ID              uuid.UUID
	Kind            Kind
	SourceMessageID uuid.UUID
	To              Recipient
	// Email — служебный ящик подразделения; пусто — адресат не настроен.
	Email         string
	Subject       string
	Facts         Facts
	Status        Status
	Attempts      int
	NextAttemptAt time.Time
	LastError     string
	CreatedAt     time.Time
	SentAt        *time.Time
}

// ErrNoRecipient — текст причины для записи без адресата.
const ErrNoRecipient = "адресат не настроен в справочнике (ADR-0021)"

// NewNotification создаёт запись по черновику. Без адреса запись сразу FAILED: ждать нечего, а потерять уведомление
// молча нельзя — ИТ-служба увидит его в журнале.
func NewNotification(id, sourceMessageID uuid.UUID, d Draft, email, subject string, now time.Time) Notification {
	n := Notification{
		ID: id, Kind: d.Kind, SourceMessageID: sourceMessageID, To: d.To, Email: email, Subject: subject,
		Facts: d.Facts, Status: StatusPending, NextAttemptAt: now, CreatedAt: now,
	}
	if email == "" {
		n.Status = StatusFailed
		n.LastError = ErrNoRecipient
	}
	return n
}
