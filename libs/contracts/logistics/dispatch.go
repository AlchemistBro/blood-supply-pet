package logistics

// Контракты этапа 7: отправка груза службой перевозки (ADR-0022). Ответ в logistics.replies, ключ — obligation_id.

import (
	"time"

	"github.com/google/uuid"
)

// CourierRefPattern — шаблон служебного идентификатора работника службы перевозки: без пробелов, значит без ФИО.
const CourierRefPattern = `^[A-Za-z0-9._:-]{1,64}$`

// TransportDispatched — уполномоченный работник службы перевозки принял груз и отправился (ПП РФ № 641, п. 73;
// ADR-0022). CourierRef — служебный идентификатор работника, без ФИО.
type TransportDispatched struct {
	TemperatureAtDispatchC *float64  `json:"temperature_at_dispatch_c,omitempty"`
	ContainerIntact        *bool     `json:"container_intact,omitempty"`
	ObligationID           uuid.UUID `json:"obligation_id"`
	ShipmentRef            string    `json:"shipment_ref,omitempty"`
	DispatchedAt           time.Time `json:"dispatched_at"`
	CourierRef             string    `json:"courier_ref"`
}

// MessageType реализует contracts.Payload.
func (TransportDispatched) MessageType() string { return "TransportDispatched" }

// SchemaVersion реализует contracts.Payload.
func (TransportDispatched) SchemaVersion() int { return 1 }
