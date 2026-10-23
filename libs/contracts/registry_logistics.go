package contracts

import "github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"

// logistics.commands и logistics.replies
func init() {
	register(
		// команды
		func() Payload { return &logistics.RequestTransport{} },
		func() Payload { return &logistics.CancelTransport{} },
		// ответы шлюза
		func() Payload { return &logistics.TransportAccepted{} },
		func() Payload { return &logistics.TransportRejected{} },
		func() Payload { return &logistics.TransportCancelled{} },
		func() Payload { return &logistics.TransportDelivered{} },
	)
}
