package transfer

// Контракты этапа 7: отправка и доставка по обязательству, ручная перевозка и закрытие эскалации дежурным
// (ADR-0022, ADR-0023). Топик transfer.events, ключ — obligation_id.

import (
	"time"

	"github.com/google/uuid"
)

// Нарушения условий перевозки (ПП РФ № 641, п. 69 в, г, е): длительность, целостность контейнера, температура
// вне режима или не зарегистрирована. При любом нарушении компонент к клиническому использованию не допускается
// (п. 98): единицы блокируются, дежурному уходит эскалация.
const (
	ViolationTemperature            = "TEMPERATURE"
	ViolationContainer              = "CONTAINER"
	ViolationDuration               = "DURATION"
	ViolationTemperatureNotRecorded = "TEMPERATURE_NOT_RECORDED"
)

// TransportViolations — все виды нарушений в порядке схемы.
var TransportViolations = []string{ViolationTemperature, ViolationContainer, ViolationDuration, ViolationTemperatureNotRecorded}

// Как дежурный закрыл эскалацию (EscalationClosed).
const (
	ResolutionManualTransport  = "MANUAL_TRANSPORT_ASSIGNED"
	ResolutionResolved         = "RESOLVED"
	ResolutionNoActionRequired = "NO_ACTION_REQUIRED"
)

// EscalationResolutions — все исходы закрытия эскалации в порядке схемы.
var EscalationResolutions = []string{ResolutionManualTransport, ResolutionResolved, ResolutionNoActionRequired}

// TransferDispatched — груз отправлен (TRANSPORT_REQUESTED → DISPATCHED); закрепления переходят в CONSUMED.
type TransferDispatched struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	// ObligationVersion — версия O07 после перехода: проекции применяют событие, только если версия выше сохранённой.
	ObligationVersion *int64 `json:"obligation_version,omitempty"`
	// SourceClinicID и TargetClinicID — клиники обязательства, необязательны: по ним web фильтрует живые статусы,
	// не держа индекс «обязательство → клиники» в памяти.
	SourceClinicID *int64    `json:"source_clinic_id,omitempty"`
	TargetClinicID *int64    `json:"target_clinic_id,omitempty"`
	RequestID      uuid.UUID `json:"request_id"`
	At             time.Time `json:"at"`
}

// MessageType реализует contracts.Payload.
func (TransferDispatched) MessageType() string { return "TransferDispatched" }

// SchemaVersion реализует contracts.Payload.
func (TransferDispatched) SchemaVersion() int { return 1 }

// TransferDelivered — груз доставлен и принят получателем (DISPATCHED → DELIVERED). ConditionsViolated —
// условия перевозки нарушены (ПП РФ № 641, п. 69 в, г, е): компонент к клиническому использованию не допускается
// (п. 98), Violations перечисляет нарушения.
type TransferDelivered struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	// ObligationVersion — версия O07 после перехода: проекции применяют событие, только если версия выше сохранённой.
	ObligationVersion *int64 `json:"obligation_version,omitempty"`
	// SourceClinicID и TargetClinicID — клиники обязательства, необязательны: по ним web фильтрует живые статусы,
	// не держа индекс «обязательство → клиники» в памяти.
	SourceClinicID     *int64    `json:"source_clinic_id,omitempty"`
	TargetClinicID     *int64    `json:"target_clinic_id,omitempty"`
	RequestID          uuid.UUID `json:"request_id"`
	At                 time.Time `json:"at"`
	ConditionsViolated *bool     `json:"conditions_violated,omitempty"`
	Violations         []string  `json:"violations,omitempty"`
}

// MessageType реализует contracts.Payload.
func (TransferDelivered) MessageType() string { return "TransferDelivered" }

// SchemaVersion реализует contracts.Payload.
func (TransferDelivered) SchemaVersion() int { return 1 }

// ManualTransportAssigned — дежурный назначил перевозку вручную после отказа или неизвестного исхода заказа
// (ADR-0023). TransportRef — номер перевозки, введённый дежурным.
type ManualTransportAssigned struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	// ObligationVersion — версия O07 после перехода: проекции применяют событие, только если версия выше сохранённой.
	ObligationVersion *int64 `json:"obligation_version,omitempty"`
	// SourceClinicID и TargetClinicID — клиники обязательства, необязательны: по ним web фильтрует живые статусы,
	// не держа индекс «обязательство → клиники» в памяти.
	SourceClinicID *int64    `json:"source_clinic_id,omitempty"`
	TargetClinicID *int64    `json:"target_clinic_id,omitempty"`
	RequestID      uuid.UUID `json:"request_id"`
	TransportRef   string    `json:"transport_ref"`
	AssignedBy     string    `json:"assigned_by"`
	At             time.Time `json:"at"`
}

// MessageType реализует contracts.Payload.
func (ManualTransportAssigned) MessageType() string { return "ManualTransportAssigned" }

// SchemaVersion реализует contracts.Payload.
func (ManualTransportAssigned) SchemaVersion() int { return 1 }

// EscalationClosed — дежурный закрыл эскалацию по обязательству (ADR-0023). Комментарий дежурного в событие
// не попадает (свободный текст может содержать ПДн): он остаётся в базе transfer и в аудите.
type EscalationClosed struct {
	ObligationID uuid.UUID `json:"obligation_id"`
	// ObligationVersion — версия O07 после перехода: проекции применяют событие, только если версия выше сохранённой.
	ObligationVersion *int64 `json:"obligation_version,omitempty"`
	// SourceClinicID и TargetClinicID — клиники обязательства, необязательны: по ним web фильтрует живые статусы,
	// не держа индекс «обязательство → клиники» в памяти.
	SourceClinicID *int64    `json:"source_clinic_id,omitempty"`
	TargetClinicID *int64    `json:"target_clinic_id,omitempty"`
	RequestID      uuid.UUID `json:"request_id"`
	Reason         string    `json:"reason,omitempty"`
	Resolution     string    `json:"resolution"`
	ClosedBy       string    `json:"closed_by"`
	At             time.Time `json:"at"`
}

// MessageType реализует contracts.Payload.
func (EscalationClosed) MessageType() string { return "EscalationClosed" }

// SchemaVersion реализует contracts.Payload.
func (EscalationClosed) SchemaVersion() int { return 1 }
