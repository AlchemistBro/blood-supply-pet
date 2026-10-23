package logistics

// Контракты этапа 5: оценки времени доставки (ADR-0014). Шлюз публикует их событием в logistics.events
// (ключ — source_clinic_id), planning и matching держат проекцию; обновить оценки можно командой
// RefreshDeliveryEstimates в logistics.commands.

import (
	"time"

	"github.com/google/uuid"
)

// DeliveryEstimatesPublished — оценки времени доставки от источника до клиник-получателей (ADR-0014).
type DeliveryEstimatesPublished struct {
	SourceClinicID int64              `json:"source_clinic_id"`
	Estimates      []DeliveryEstimate `json:"estimates"`
	EstimatedAt    time.Time          `json:"estimated_at"`
}

// DeliveryEstimate — оценка времени доставки до клиники-получателя в минутах.
type DeliveryEstimate struct {
	TargetClinicID int64 `json:"target_clinic_id"`
	Minutes        int   `json:"minutes"`
}

// MessageType реализует contracts.Payload.
func (DeliveryEstimatesPublished) MessageType() string { return "DeliveryEstimatesPublished" }

// SchemaVersion реализует contracts.Payload.
func (DeliveryEstimatesPublished) SchemaVersion() int { return 1 }

// RefreshDeliveryEstimates — команда шлюзу запросить у C03 свежие оценки доставки. Пустой список источников —
// по всем клиникам. RequestID — ключ идемпотентности.
type RefreshDeliveryEstimates struct {
	RequestID       uuid.UUID `json:"request_id"`
	SourceClinicIDs []int64   `json:"source_clinic_ids,omitempty"`
	RequestedAt     time.Time `json:"requested_at"`
}

// MessageType реализует contracts.Payload.
func (RefreshDeliveryEstimates) MessageType() string { return "RefreshDeliveryEstimates" }

// SchemaVersion реализует contracts.Payload.
func (RefreshDeliveryEstimates) SchemaVersion() int { return 1 }
