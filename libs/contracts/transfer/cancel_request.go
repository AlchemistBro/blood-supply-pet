package transfer

import "github.com/google/uuid"

// CancelRequestObligations отменяет задания заявки/плана и запрещает позднее открытие (ADR-0030).
type CancelRequestObligations struct {
	RequestID   uuid.UUID `json:"request_id"`
	Reason      string    `json:"reason"`
	OnlyPending bool      `json:"only_pending"`
}

func (CancelRequestObligations) MessageType() string { return "CancelRequestObligations" }
func (CancelRequestObligations) SchemaVersion() int  { return 1 }
