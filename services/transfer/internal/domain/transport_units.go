package domain

import (
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
)

// TransportUnitViolations проверяет известные метаданные при приёмке, отдельно от температурного режима.
func TransportUnitViolations(o Obligation, ids []uuid.UUID, copies map[uuid.UUID]UnitCopy, at time.Time) []string {
	violations := []string{}
	add := func(code string) {
		if !slices.Contains(violations, code) {
			violations = append(violations, code)
		}
	}
	if len(ids) == 0 || len(ids) != o.ConfirmedQty {
		add(transfer.ViolationUnitUnavailable)
	}
	for _, id := range ids {
		unit, ok := copies[id]
		if !ok {
			add(transfer.ViolationUnitUnavailable)
			continue
		}
		if !unit.ExpiresAt.After(at) {
			add(transfer.ViolationUnitExpired)
		}
		status := unit.SourceStatus
		if status == "" {
			status = unit.Status
		}
		if status != UnitInStock && status != UnitTransferred {
			add(transfer.ViolationUnitUnavailable)
		}
		if unit.ClinicID != o.SourceClinicID && unit.ClinicID != o.TargetClinicID {
			add(transfer.ViolationUnitUnavailable)
		}
		// После учёта приёмки клиника уже может быть получателем; исходная спецификация остаётся той же.
		unit.ClinicID = o.SourceClinicID
		if o.Spec.Mismatch(o.SourceClinicID, o.ComponentKind, unit) != "" {
			add(transfer.ViolationSpecificationChanged)
		}
	}
	return violations
}
