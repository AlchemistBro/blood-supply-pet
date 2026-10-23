// Package logistics — контракт шлюза перевозчика (C03, ИНТ-03). Команды RequestTransport и CancelTransport
// пишет transfer в logistics.commands, ответы TransportAccepted, TransportRejected, TransportCancelled и
// TransportDelivered шлюз пишет в logistics.replies. Ключ — obligation_id: команды и ответы по одному
// обязательству читаются по порядку.
//
// Номеров единиц в командах нет: перевозчику нужны маршрут, вид, число контейнеров и температурный режим,
// поэтому замена единицы до отправки (ADR-0011) не требует новой команды.
package logistics

import (
	"time"

	"github.com/google/uuid"
)

// Температурный режим перевозки. ПП РФ № 641 требует соблюдать условия транспортировки компонентов крови;
// конкретные диапазоны здесь демонстрационные (ADR-0010).
const (
	RegimeCold2to6      = "COLD_2_6"
	RegimeFrozenMinus25 = "FROZEN_MINUS_25"
	RegimeRoom20to24    = "ROOM_20_24"
)

// demoRegimes — режим по коду вида компонента (ref_component_kind). Демо: заказчик заменяет при внедрении.
var demoRegimes = map[string]string{
	"RBC":  RegimeCold2to6,
	"FFP":  RegimeFrozenMinus25,
	"CRYO": RegimeFrozenMinus25,
	"PLT":  RegimeRoom20to24,
}

// TemperatureRegimeFor возвращает демонстрационный режим перевозки для вида компонента.
// Неизвестный вид — false: режим по умолчанию опаснее, чем отказ собрать команду.
func TemperatureRegimeFor(componentKind string) (string, bool) {
	r, ok := demoRegimes[componentKind]
	return r, ok
}

// Причины отказа в перевозке (TransportRejected).
const (
	// RejectRefused — перевозчик отказал.
	RejectRefused = "REFUSED"
	// RejectOutcomeUnknown — честное «не знаем»: C03 долго недоступна и шлюз не смог проверить, дошёл ли заказ.
	// Если сверка потом найдёт заказ, придёт запоздалый TransportAccepted.
	RejectOutcomeUnknown = "OUTCOME_UNKNOWN"
)

// Причины снятия заказа (CancelTransport).
const (
	CancelUnitWithdrawn       = "UNIT_WITHDRAWN"
	CancelObligationCancelled = "OBLIGATION_CANCELLED"
)

// RequestTransport — команда «заказать перевозку» по подтверждённому обязательству.
type RequestTransport struct {
	ObligationID   uuid.UUID `json:"obligation_id"`
	RequestID      uuid.UUID `json:"request_id"`
	SourceClinicID int64     `json:"source_clinic_id"`
	TargetClinicID int64     `json:"target_clinic_id"`
	ComponentKind  string    `json:"component_kind"`
	// Quantity — число контейнеров (единиц) без их номеров.
	Quantity          int    `json:"quantity"`
	TemperatureRegime string `json:"temperature_regime"`
}

// MessageType реализует contracts.Payload.
func (RequestTransport) MessageType() string { return "RequestTransport" }

// SchemaVersion реализует contracts.Payload.
func (RequestTransport) SchemaVersion() int { return 1 }

// CancelTransport — компенсация: обязательство нарушено или отменено, заказ надо снять, если он был.
// Шлюз отвечает TransportCancelled и тогда, когда заказа не было.
type CancelTransport struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	Reason       string    `json:"reason"`
}

// MessageType реализует contracts.Payload.
func (CancelTransport) MessageType() string { return "CancelTransport" }

// SchemaVersion реализует contracts.Payload.
func (CancelTransport) SchemaVersion() int { return 1 }

// TransportAccepted — перевозчик принял заказ.
type TransportAccepted struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	ShipmentRef  string    `json:"shipment_ref"`
}

// MessageType реализует contracts.Payload.
func (TransportAccepted) MessageType() string { return "TransportAccepted" }

// SchemaVersion реализует contracts.Payload.
func (TransportAccepted) SchemaVersion() int { return 1 }

// TransportRejected — заказ перевозки не состоялся.
type TransportRejected struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	Reason       string    `json:"reason"`
	Details      string    `json:"details,omitempty"`
}

// MessageType реализует contracts.Payload.
func (TransportRejected) MessageType() string { return "TransportRejected" }

// SchemaVersion реализует contracts.Payload.
func (TransportRejected) SchemaVersion() int { return 1 }

// TransportCancelled — заказ снят или так и не был создан: перевозки по обязательству не будет.
type TransportCancelled struct {
	ObligationID uuid.UUID `json:"obligation_id"`
}

// MessageType реализует contracts.Payload.
func (TransportCancelled) MessageType() string { return "TransportCancelled" }

// SchemaVersion реализует contracts.Payload.
func (TransportCancelled) SchemaVersion() int { return 1 }

// TransportDelivered — перевозчик доставил груз. Температура при отправке и при приёме необязательна:
// перевозчик может её не сообщить. Основание — ПП РФ № 641 (регистрация температуры при перевозке).
type TransportDelivered struct {
	ObligationID           uuid.UUID `json:"obligation_id"`
	DeliveredAt            time.Time `json:"delivered_at"`
	TemperatureAtDispatchC *float64  `json:"temperature_at_dispatch_c,omitempty"`
	TemperatureAtArrivalC  *float64  `json:"temperature_at_arrival_c,omitempty"`
}

// MessageType реализует contracts.Payload.
func (TransportDelivered) MessageType() string { return "TransportDelivered" }

// SchemaVersion реализует contracts.Payload.
func (TransportDelivered) SchemaVersion() int { return 1 }
