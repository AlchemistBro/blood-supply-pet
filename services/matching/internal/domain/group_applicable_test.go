package domain_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/domain"
)

func TestGroupIndependentKindDoesNotRequireUnitBloodGroup(t *testing.T) {
	request := domain.Request{RequestID: uuid.New(), ClinicID: 1, ComponentKind: "CRYO", RequestedUnits: 1, Urgency: "PLANNED", BloodGroup: "A+", CreatedAt: at}
	result, err := domain.Match(domain.Input{Request: request, At: at, Rules: demoRules(t), GroupApplicable: map[string]bool{"CRYO": false}, Units: []domain.Unit{{UnitID: uuid.New(), ClinicID: 1, ComponentKind: "CRYO", VolumeML: 20, ExpiresAt: at.Add(24 * time.Hour), Status: domain.StatusInStock}}})
	if err != nil || len(result.Variants) != 1 || !result.Variants[0].IsPreferred {
		t.Fatalf("CRYO ошибочно ограничен группой пациента: %+v %v", result, err)
	}
}
