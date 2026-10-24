package contracts

import "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"

// audit.events
func init() {
	register(
		func() Payload { return &audit.AuditRecorded{} },
	)
}
