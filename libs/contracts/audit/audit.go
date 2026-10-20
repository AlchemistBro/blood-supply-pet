// Package audit — контракт журнала аудита (NFR008, NFR016). Каждый сервис пишет AuditRecorded
// в свой outbox в транзакции бизнес-операции; сервис audit складывает записи в неизменяемый журнал.
package audit

import "time"

// Результат действия.
const (
	ResultSuccess = "SUCCESS"
	ResultDenied  = "DENIED"
	ResultError   = "ERROR"
)

// Модули, от имени которых пишется аудит.
const (
	ModuleInventory = "inventory"
	ModuleDemand    = "demand"
	ModulePlanning  = "planning"
	ModuleMatching  = "matching"
	ModuleTransfer  = "transfer"
	ModuleLogistics = "logistics"
	ModuleWeb       = "web"
	ModuleAudit     = "audit"
)

// AuditRecorded — запись аудита: участник, время, объект, результат.
// Details не содержит ПДн и номера случая: это проверяется тестом контракта.
type AuditRecorded struct {
	OccurredAt    time.Time      `json:"occurred_at"`
	UserID        string         `json:"user_id,omitempty"`
	Role          string         `json:"role,omitempty"`
	ClinicID      *int64         `json:"clinic_id,omitempty"`
	Module        string         `json:"module"`
	Action        string         `json:"action"`
	ObjectType    string         `json:"object_type"`
	ObjectID      string         `json:"object_id"`
	Result        string         `json:"result"`
	Details       map[string]any `json:"details,omitempty"`
	CorrelationID string         `json:"correlation_id,omitempty"`
}

// MessageType реализует contracts.Payload.
func (AuditRecorded) MessageType() string { return "AuditRecorded" }

// SchemaVersion реализует contracts.Payload.
func (AuditRecorded) SchemaVersion() int { return 1 }

// Действия, общие для всех сервисов. Специфичные объявляются в сервисах.
const (
	ActionLogin           = "LOGIN"
	ActionLogout          = "LOGOUT"
	ActionAccessDenied    = "ACCESS_DENIED"
	ActionReadPatientCase = "READ_PATIENT_CASE"
)
