package contracts

import (
	"strconv"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
)

// Registry — все типы сообщений: имя типа → конструктор пустого значения.
// Используется проверкой контрактов (пример → Go-тип → JSON → схема) и consumer-ами для декодирования.
var Registry = map[string]func() Payload{
	"AuditRecorded": func() Payload { return &audit.AuditRecorded{} },

	// transfer.commands
	"OpenObligation":   func() Payload { return &transfer.OpenObligation{} },
	"CancelObligation": func() Payload { return &transfer.CancelObligation{} },
	// transfer.events
	"ObligationOpened":          func() Payload { return &transfer.ObligationOpened{} },
	"TransferConfirmed":         func() Payload { return &transfer.TransferConfirmed{} },
	"TransferShortfallRecorded": func() Payload { return &transfer.TransferShortfallRecorded{} },
	"TransferRejected":          func() Payload { return &transfer.TransferRejected{} },
	"TransferExpired":           func() Payload { return &transfer.TransferExpired{} },
	"TransferCancelled":         func() Payload { return &transfer.TransferCancelled{} },
	"TransferBroken":            func() Payload { return &transfer.TransferBroken{} },
	"TransferUnitReplaced":      func() Payload { return &transfer.TransferUnitReplaced{} },
	"TransportRequested":        func() Payload { return &transfer.TransportRequested{} },
	"TransportRequestFailed":    func() Payload { return &transfer.TransportRequestFailed{} },
	"EscalationRaised":          func() Payload { return &transfer.EscalationRaised{} },
}

// SchemaPath возвращает путь к JSON Schema типа относительно libs/contracts.
func SchemaPath(topicDir, msgType string, version int) string {
	return "schemas/" + topicDir + "/" + msgType + ".v" + strconv.Itoa(version) + ".json"
}
