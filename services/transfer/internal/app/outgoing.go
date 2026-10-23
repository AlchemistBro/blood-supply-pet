package app

import (
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
)

// Исходящие события и команды transfer — типы опубликованного языка из libs/contracts.
// Псевдонимы держат сценарии короткими и собирают все исходящие типы в одном месте.
type (
	ObligationOpened          = transfer.ObligationOpened
	TransferConfirmed         = transfer.TransferConfirmed
	TransferShortfallRecorded = transfer.TransferShortfallRecorded
	TransferRejected          = transfer.TransferRejected
	TransferExpired           = transfer.TransferExpired
	TransferCancelled         = transfer.TransferCancelled
	TransferBroken            = transfer.TransferBroken
	TransferUnitReplaced      = transfer.TransferUnitReplaced
	TransportRequested        = transfer.TransportRequested
	TransportRequestFailed    = transfer.TransportRequestFailed
	EscalationRaised          = transfer.EscalationRaised
	RequestTransport          = logistics.RequestTransport
	CancelTransport           = logistics.CancelTransport
)
