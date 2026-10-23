package app

import (
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
)

// Исходящие события и команды transfer.
//
// ВРЕМЕННО: типы контрактов этапа 1 появляются в libs/contracts/{transfer,logistics} параллельно (пакет WP1).
// До слияния здесь лежат структуры с теми же именами и полями (docs/architecture/events.md). После слияния
// этот файл заменяется псевдонимами на типы libs/contracts одной правкой; остальной код app не меняется.

// ObligationOpened — задание открыто у источника.
type ObligationOpened struct {
	ObligationID   uuid.UUID `json:"obligation_id"`
	RequestID      uuid.UUID `json:"request_id"`
	Origin         string    `json:"origin"`
	SourceClinicID int64     `json:"source_clinic_id"`
	TargetClinicID int64     `json:"target_clinic_id"`
	ComponentKind  string    `json:"component_kind"`
	RequestedQty   int       `json:"requested_qty"`
	Deadline       time.Time `json:"deadline"`
}

// TransferConfirmed — единицы закреплены оператором источника.
type TransferConfirmed struct {
	ObligationID uuid.UUID   `json:"obligation_id"`
	RequestID    uuid.UUID   `json:"request_id"`
	Units        []uuid.UUID `json:"units"`
	Quantity     int         `json:"quantity"`
	Operator     string      `json:"operator"`
}

// TransferShortfallRecorded — частичное подтверждение: недостача на остаток.
type TransferShortfallRecorded struct {
	ObligationID  uuid.UUID `json:"obligation_id"`
	RequestID     uuid.UUID `json:"request_id"`
	ComponentKind string    `json:"component_kind"`
	Shortfall     int       `json:"shortfall"`
}

// TransferRejected — все выбранные единицы недоступны.
type TransferRejected struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	RequestID    uuid.UUID `json:"request_id"`
	Reason       string    `json:"reason"`
}

// TransferExpired — срок подтверждения истёк.
type TransferExpired struct {
	ObligationID     uuid.UUID  `json:"obligation_id"`
	RequestID        uuid.UUID  `json:"request_id"`
	NextObligationID *uuid.UUID `json:"next_obligation_id,omitempty"`
}

// TransferCancelled — задание отменено.
type TransferCancelled struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	RequestID    uuid.UUID `json:"request_id"`
	Reason       string    `json:"reason"`
}

// TransferBroken — закреплённая единица выбыла или на карантине.
type TransferBroken struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	RequestID    uuid.UUID `json:"request_id"`
	UnitID       uuid.UUID `json:"unit_id"`
	Reason       string    `json:"reason"`
}

// TransferUnitReplaced — единица заменена равноценной (ADR-0011). Спецификации и фенотипа здесь нет.
type TransferUnitReplaced struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	OldUnitID    uuid.UUID `json:"old_unit_id"`
	NewUnitID    uuid.UUID `json:"new_unit_id"`
	RuleVersion  string    `json:"rule_version"`
}

// TransportRequested — перевозчик принял заказ.
type TransportRequested struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	ShipmentRef  string    `json:"shipment_ref"`
}

// TransportRequestFailed — перевозчик отказал; перевозку организует дежурный.
type TransportRequestFailed struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	Reason       string    `json:"reason"`
	Details      string    `json:"details,omitempty"`
}

// EscalationRaised — нужен дежурный смены.
type EscalationRaised struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	RequestID    uuid.UUID `json:"request_id"`
	Reason       string    `json:"reason"`
}

// RequestTransport — команда шлюзу логистики. Номеров единиц нет: замена единицы заказ не трогает.
type RequestTransport struct {
	ObligationID      uuid.UUID `json:"obligation_id"`
	RequestID         uuid.UUID `json:"request_id"`
	SourceClinicID    int64     `json:"source_clinic_id"`
	TargetClinicID    int64     `json:"target_clinic_id"`
	ComponentKind     string    `json:"component_kind"`
	Quantity          int       `json:"quantity"`
	TemperatureRegime string    `json:"temperature_regime"`
}

// CancelTransport — компенсация саги: снять заказ перевозки.
type CancelTransport struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	Reason       string    `json:"reason"`
}

func (ObligationOpened) MessageType() string          { return "ObligationOpened" }
func (TransferConfirmed) MessageType() string         { return "TransferConfirmed" }
func (TransferShortfallRecorded) MessageType() string { return "TransferShortfallRecorded" }
func (TransferRejected) MessageType() string          { return "TransferRejected" }
func (TransferExpired) MessageType() string           { return "TransferExpired" }
func (TransferCancelled) MessageType() string         { return "TransferCancelled" }
func (TransferBroken) MessageType() string            { return "TransferBroken" }
func (TransferUnitReplaced) MessageType() string      { return "TransferUnitReplaced" }
func (TransportRequested) MessageType() string        { return "TransportRequested" }
func (TransportRequestFailed) MessageType() string    { return "TransportRequestFailed" }
func (EscalationRaised) MessageType() string          { return "EscalationRaised" }
func (RequestTransport) MessageType() string          { return "RequestTransport" }
func (CancelTransport) MessageType() string           { return "CancelTransport" }

func (ObligationOpened) SchemaVersion() int          { return 1 }
func (TransferConfirmed) SchemaVersion() int         { return 1 }
func (TransferShortfallRecorded) SchemaVersion() int { return 1 }
func (TransferRejected) SchemaVersion() int          { return 1 }
func (TransferExpired) SchemaVersion() int           { return 1 }
func (TransferCancelled) SchemaVersion() int         { return 1 }
func (TransferBroken) SchemaVersion() int            { return 1 }
func (TransferUnitReplaced) SchemaVersion() int      { return 1 }
func (TransportRequested) SchemaVersion() int        { return 1 }
func (TransportRequestFailed) SchemaVersion() int    { return 1 }
func (EscalationRaised) SchemaVersion() int          { return 1 }
func (RequestTransport) SchemaVersion() int          { return 1 }
func (CancelTransport) SchemaVersion() int           { return 1 }

// Проверка на этапе компиляции: все типы — полезные нагрузки outbox.
var _ = []contracts.Payload{
	ObligationOpened{}, TransferConfirmed{}, TransferShortfallRecorded{}, TransferRejected{}, TransferExpired{},
	TransferCancelled{}, TransferBroken{}, TransferUnitReplaced{}, TransportRequested{}, TransportRequestFailed{},
	EscalationRaised{}, RequestTransport{}, CancelTransport{},
}
