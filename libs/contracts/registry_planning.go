package contracts

import "github.com/AlchemistBro/blood-supply-pet/libs/contracts/planning"

// planning.events
func init() {
	register(
		func() Payload { return &planning.EmergencyForecastAccepted{} },
		func() Payload { return &planning.ReplenishmentPlanBuilt{} },
		func() Payload { return &planning.TransferProposed{} },
		func() Payload { return &planning.PlanDeficitRecorded{} },
		func() Payload { return &planning.ProtectedVolumeCalculated{} },
	)
}
