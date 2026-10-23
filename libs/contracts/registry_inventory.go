package contracts

import "github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"

// inventory.events
func init() {
	register(
		func() Payload { return &inventory.UnitRegistered{} },
		func() Payload { return &inventory.UnitUpdated{} },
		func() Payload { return &inventory.UnitWithdrawn{} },
		func() Payload { return &inventory.ProtectedVolumePublished{} },
	)
}
