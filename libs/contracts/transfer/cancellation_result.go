package transfer

import (
	"github.com/google/uuid"
	"time"
)

// RequestCancellationProcessed завершает запрос отмены. Непустой список означает отправленную часть.
type RequestCancellationProcessed struct {
	RequestID                   uuid.UUID   `json:"request_id"`
	NonCancellableObligationIDs []uuid.UUID `json:"non_cancellable_obligation_ids"`
	At                          time.Time   `json:"at"`
}

func (RequestCancellationProcessed) MessageType() string { return "RequestCancellationProcessed" }
func (RequestCancellationProcessed) SchemaVersion() int  { return 1 }
