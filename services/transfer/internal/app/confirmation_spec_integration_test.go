//go:build integration

package app_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

func TestInitialConfirmationChecksGroupAndArrivalExpiry(t *testing.T) {
	h := setup(t)
	unit := h.registerUnit(1, "RBC", "A", true, h.inDays(20))
	group := "O+"
	minimum := h.inDays(1)
	n := newObligation(1, "RBC", 1, nil, domain.ReplacementSpec{RuleVersion: rule, BloodGroup: &group, MinExpiresAt: &minimum})
	id := h.openWith(n)
	if res := h.confirm(id, []uuid.UUID{unit}, false); !isType[domain.InvalidRequest](res) {
		t.Fatalf("чужая группа подтверждена: %#v", res)
	}
	group = "A+"
	minimum = h.inDays(20)
	n.ChoiceID = uuid.New()
	n.Spec.BloodGroup = &group
	n.Spec.MinExpiresAt = &minimum
	id = h.openWith(n)
	if res := h.confirm(id, []uuid.UUID{unit}, false); !isType[domain.InvalidRequest](res) {
		t.Fatalf("годность равна границе прибытия: %#v", res)
	}
	h.assertStockConsistent(1, "RBC")
}
