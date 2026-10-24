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

// Температурный режим перевозки. ПП РФ № 641, п. 67–69: перевозка раздельно по температурным режимам (п. 68 в),
// температура при отправке и по прибытии (п. 69 е). Код — метка режима, а не норматив: предельные значения при
// перевозке задаёт приложение 3 ПП 641 (эритроцитсодержащие — не выше +10 °C, плазма и криопреципитат — не выше
// −18 °C, тромбоциты — +20…+24 °C). Коды не переименовываются: на них уже опирается имитатор перевозчика.
const (
	RegimeCold2to6      = "COLD_2_6"
	RegimeFrozenMinus25 = "FROZEN_MINUS_25"
	RegimeRoom20to24    = "ROOM_20_24"
)

// Regimes — все коды температурного режима в порядке схемы.
var Regimes = []string{RegimeCold2to6, RegimeFrozenMinus25, RegimeRoom20to24}

// demoRegimes — режим по коду вида компонента (ref_component_kind); соответствие вида и режима — по приложению 3 ПП 641.
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

// RejectReasons — все причины отказа в порядке схемы.
var RejectReasons = []string{RejectRefused, RejectOutcomeUnknown}

// Причины снятия заказа (CancelTransport). UNIT_QUARANTINED — закреплённая единица получила в базе статус
// «на карантине» (ПП РФ № 641, п. 62 а); снимать заказ в этом случае — решение проекта (ADR-0027).
const (
	CancelUnitWithdrawn       = "UNIT_WITHDRAWN"
	CancelUnitQuarantined     = "UNIT_QUARANTINED"
	CancelObligationCancelled = "OBLIGATION_CANCELLED"
)

// CancelReasons — все причины снятия заказа в порядке схемы.
var CancelReasons = []string{CancelUnitWithdrawn, CancelUnitQuarantined, CancelObligationCancelled}

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

// TransportDelivered — перевозчик доставил груз. Температура при отправке и по прибытии (ПП РФ № 641, п. 69 е:
// обязательна при перевозке дольше 30 минут) в контракте необязательна: служба перевозки может её не сообщить,
// и тогда пропуск показывается как нарушение данных (ADR-0022).
type TransportDelivered struct {
	ShipmentRef            string    `json:"shipment_ref,omitempty"`
	ObligationID           uuid.UUID `json:"obligation_id"`
	DeliveredAt            time.Time `json:"delivered_at"`
	TemperatureAtDispatchC *float64  `json:"temperature_at_dispatch_c,omitempty"`
	TemperatureAtArrivalC  *float64  `json:"temperature_at_arrival_c,omitempty"`
	// Этап 7 (ADR-0022), все поля необязательные — обратная совместимость с контрактом этапа 1.
	// DepartedAt и DurationMin — длительность от выдачи до места назначения (п. 69 в).
	DepartedAt  *time.Time `json:"departed_at,omitempty"`
	DurationMin *int       `json:"duration_min,omitempty"`
	// ContainerIntact — целостность контейнера при приёме (п. 69 г).
	ContainerIntact *bool `json:"container_intact,omitempty"`
	// TempExcursion — температура выходила за режим перевозки (п. 69 е, приложение 3); п. 98 — к использованию не допускается.
	TempExcursion *bool `json:"temp_excursion,omitempty"`
	// CourierRef — служебный идентификатор уполномоченного работника (п. 73), без ФИО.
	CourierRef string `json:"courier_ref,omitempty"`
}

// MessageType реализует contracts.Payload.
func (TransportDelivered) MessageType() string { return "TransportDelivered" }

// SchemaVersion реализует contracts.Payload.
func (TransportDelivered) SchemaVersion() int { return 1 }
