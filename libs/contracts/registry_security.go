package contracts

import "github.com/AlchemistBro/blood-supply-pet/libs/contracts/security"

// security.events
func init() {
	register(
		func() Payload { return &security.SecurityIncidentSuspected{} },
	)
}
