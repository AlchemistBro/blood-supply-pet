// Package security — контракт событий безопасности (топик security.events). Детектор аномалий в журнале аудита
// публикует подозрение на инцидент с персональными данными: по 152-ФЗ ст. 21 ч. 3.1 оператор уведомляет
// Роскомнадзор в течение 24 часов и сообщает результаты расследования в течение 72 часов (ADR-0025).
//
// В событии нет ПДн: только код правила, учётная запись, число срабатываний и окно наблюдения.
package security

import (
	"time"

	"github.com/google/uuid"
)

// SecurityIncidentSuspected — сработало правило детектора: например, всплеск чтений номера случая или отказов
// в доступе одной учётной записью. Count срабатываний за WindowSeconds; часы 24/72 ч отсчитываются от DetectedAt.
type SecurityIncidentSuspected struct {
	IncidentID    uuid.UUID  `json:"incident_id"`
	Rule          string     `json:"rule"`
	UserID        string     `json:"user_id"`
	Count         int        `json:"count"`
	WindowSeconds int        `json:"window_seconds"`
	FirstSeenAt   *time.Time `json:"first_seen_at,omitempty"`
	DetectedAt    time.Time  `json:"detected_at"`
}

// MessageType реализует contracts.Payload.
func (SecurityIncidentSuspected) MessageType() string { return "SecurityIncidentSuspected" }

// SchemaVersion реализует contracts.Payload.
func (SecurityIncidentSuspected) SchemaVersion() int { return 1 }
