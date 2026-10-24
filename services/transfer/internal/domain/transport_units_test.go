package domain_test

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

func TestArrivalChecksSourceStatusExpiryAndSpecification(t *testing.T) {
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	id := uuid.New()
	group := "O+"
	o := domain.Obligation{SourceClinicID: 1, TargetClinicID: 2, ComponentKind: "RBC", ConfirmedQty: 1, Spec: domain.ReplacementSpec{BloodGroup: &group}}
	unit := domain.UnitCopy{UnitID: id, ClinicID: 2, ComponentKind: "RBC", BloodGroup: "O+", RhPositive: true, Status: domain.UnitTransferred, SourceStatus: domain.UnitInStock, ExpiresAt: at.Add(time.Hour)}
	if violations := domain.TransportUnitViolations(o, []uuid.UUID{id}, map[uuid.UUID]domain.UnitCopy{id: unit}, at); len(violations) != 0 {
		t.Fatalf("здоровая приёмка после переноса клиники заблокирована: %v", violations)
	}
	unit.SourceStatus = domain.UnitQuarantine
	unit.ExpiresAt = at
	violations := domain.TransportUnitViolations(o, []uuid.UUID{id}, map[uuid.UUID]domain.UnitCopy{id: unit}, at)
	if !slices.Contains(violations, transfer.ViolationUnitExpired) || !slices.Contains(violations, transfer.ViolationUnitUnavailable) {
		t.Fatal("пропущены истечение срока или карантин")
	}
	unit.SourceStatus = domain.UnitTransferred
	unit.ExpiresAt = at.Add(time.Hour)
	unit.BloodGroup = "A+"
	if !slices.Contains(domain.TransportUnitViolations(o, []uuid.UUID{id}, map[uuid.UUID]domain.UnitCopy{id: unit}, at), transfer.ViolationSpecificationChanged) {
		t.Fatal("смена группы не нарушила спецификацию")
	}
}
