package app

import (
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

// Входы сценариев в терминах приложения. Адаптеры (HTTP, consumer-ы Kafka) переводят в них контракты
// libs/contracts, поэтому смена формата сообщения не трогает сценарии.

// ConfirmTransfer — оператор источника подтверждает задание выбранными единицами.
type ConfirmTransfer struct {
	ObligationID uuid.UUID
	UnitIDs      []uuid.UUID
	// AcceptPartial — оператор согласен закрепить только оставшиеся на месте единицы.
	AcceptPartial  bool
	IdempotencyKey string
	Operator       string
}

// ReplaceUnit — замена закреплённой единицы равноценной до отправки (ADR-0011).
type ReplaceUnit struct {
	ObligationID uuid.UUID
	OldUnitID    uuid.UUID
	NewUnitID    uuid.UUID
	Operator     string
}

// CancelObligation — отмена задания (заявка отменена, план пересчитан).
type CancelObligation struct {
	ObligationID uuid.UUID
	Reason       string
	Actor        string
}

// UnitRegistered — единица появилась в копии запаса по данным АИСТ-М. Может прийти сразу на карантине.
type UnitRegistered struct {
	UnitID        uuid.UUID
	ClinicID      int64
	ComponentKind string
	BloodGroup    string
	RhPositive    bool
	Phenotype     *string
	VolumeML      int64
	ExpiresAt     time.Time
	Status        domain.UnitStatus
	Version       int64
}

// UnitUpdated — изменился статус единицы (в том числе карантин).
type UnitUpdated struct {
	UnitID        uuid.UUID
	ClinicID      int64
	ComponentKind string
	Status        domain.UnitStatus
	Version       int64
}

// UnitWithdrawn — единица выбыла: выдана, списана, передана, истёк срок.
type UnitWithdrawn struct {
	UnitID        uuid.UUID
	ClinicID      int64
	ComponentKind string
	Reason        string
	Version       int64
}

// ProtectedVolume — защищённый объём источника в единицах (NFR013).
type ProtectedVolume struct {
	ClinicID      int64
	ComponentKind string
	VolumeUnits   int
	Version       int64
	Reason        string
}

// ReplyKind — вид ответа шлюза логистики.
type ReplyKind string

// Ответы шлюза.
const (
	ReplyAccepted  ReplyKind = "TransportAccepted"
	ReplyRejected  ReplyKind = "TransportRejected"
	ReplyCancelled ReplyKind = "TransportCancelled"
)

// Причины TransportRejected.
const (
	RejectRefused        = logistics.RejectRefused
	RejectOutcomeUnknown = logistics.RejectOutcomeUnknown
)

// LogisticsReply — ответ logistics-gateway из logistics.replies.
type LogisticsReply struct {
	Kind         ReplyKind
	ObligationID uuid.UUID
	ShipmentRef  string
	Reason       string
	Details      string
}

// CancelResult — итог отмены. Cancelled=false — задание уже в конечном статусе, ничего не менялось.
type CancelResult struct {
	ObligationID uuid.UUID
	Cancelled    bool
	Status       domain.Status
}

// Escalation — итог таймера: старое O07 EXPIRED, новое у следующего источника или nil.
type Escalation struct {
	ExpiredID uuid.UUID
	NextID    *uuid.UUID
}

// ObligationView — обязательство с действующими закреплениями.
type ObligationView struct {
	domain.Obligation
	Units []uuid.UUID
}
