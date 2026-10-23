package contracts

import "github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"

// inventory.events
func init() {
	register(
		func() Payload { return &inventory.UnitRegistered{} },
		func() Payload { return &inventory.UnitUpdated{} },
		func() Payload { return &inventory.UnitWithdrawn{} },
		func() Payload { return &inventory.ProtectedVolumePublished{} },
		// этап 3: запас, норматив, обмен с АИСТ-М
		func() Payload { return &inventory.StockSnapshotPublished{} },
		func() Payload { return &inventory.StockNormPublished{} },
		func() Payload { return &inventory.StockBelowNorm{} },
		func() Payload { return &inventory.SyncRunCompleted{} },
		func() Payload { return &inventory.ConsumptionExportPublished{} },
		// этап 6: ИНТ-05 - команды inventory.commands и квитанция в inventory.events
		func() Payload { return &inventory.AllocateUnitsToPatient{} },
		func() Payload { return &inventory.ReleasePatientAllocation{} },
		func() Payload { return &inventory.PatientAllocationAcknowledged{} },
	)
}
