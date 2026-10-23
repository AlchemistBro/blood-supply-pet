package contracts

import "github.com/AlchemistBro/blood-supply-pet/libs/contracts/matching"

// matching.events
func init() {
	register(
		func() Payload { return &matching.ClinicalRequestCreated{} },
		func() Payload { return &matching.VariantsOffered{} },
		func() Payload { return &matching.DoctorChoiceRecorded{} },
		func() Payload { return &matching.RequestDeficitRecorded{} },
		func() Payload { return &matching.ClinicalRequestCancelled{} },
	)
}
