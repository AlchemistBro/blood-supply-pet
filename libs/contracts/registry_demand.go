package contracts

import "github.com/AlchemistBro/blood-supply-pet/libs/contracts/demand"

// demand.events
func init() {
	register(
		func() Payload { return &demand.StatisticsFileAccepted{} },
		func() Payload { return &demand.StatisticsFileRejected{} },
		func() Payload { return &demand.DemandEstimateCalculated{} },
		func() Payload { return &demand.DemandEstimatePublished{} },
		func() Payload { return &demand.ClinicSharesPublished{} },
	)
}
