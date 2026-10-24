package contracts

import "github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"

// transfer.commands и transfer.events
func init() {
	register(
		// команды
		func() Payload { return &transfer.OpenObligation{} },
		func() Payload { return &transfer.CancelObligation{} },
		func() Payload { return &transfer.CancelRequestObligations{} },
		// события
		func() Payload { return &transfer.ObligationOpened{} },
		func() Payload { return &transfer.TransferConfirmed{} },
		func() Payload { return &transfer.TransferShortfallRecorded{} },
		func() Payload { return &transfer.TransferRejected{} },
		func() Payload { return &transfer.TransferExpired{} },
		func() Payload { return &transfer.TransferCancelled{} },
		func() Payload { return &transfer.RequestCancellationProcessed{} },
		func() Payload { return &transfer.TransferBroken{} },
		func() Payload { return &transfer.TransferUnitReplaced{} },
		func() Payload { return &transfer.TransportRequested{} },
		func() Payload { return &transfer.TransportRequestFailed{} },
		func() Payload { return &transfer.EscalationRaised{} },
		// этап 7: перевозка, ручная перевозка, закрытие эскалации
		func() Payload { return &transfer.TransferDispatched{} },
		func() Payload { return &transfer.TransferDelivered{} },
		func() Payload { return &transfer.ManualTransportAssigned{} },
		func() Payload { return &transfer.EscalationClosed{} },
	)
}
