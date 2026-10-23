//go:build integration

package app_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

// Перерегистрация единицы в другой клинике пересчитывает оба Остатка.
func TestReRegistrationInAnotherClinicRecountsBothStocks(t *testing.T) {
	h := setup(t)
	unit := h.registerUnits(1, "RBC", 1)[0]
	if err := h.svc.OnUnitRegistered(h.ctx, delivery(), app.UnitRegistered{
		UnitID: unit, ClinicID: 2, ComponentKind: "RBC", BloodGroup: "O", RhPositive: true, VolumeML: 300,
		ExpiresAt: h.inDays(20), Version: h.versions.Add(1),
	}); err != nil {
		t.Fatal(err)
	}
	h.assertStockConsistent(1, "RBC")
	h.assertStockConsistent(2, "RBC")
	if n := h.count(`SELECT available_units FROM source_stock WHERE clinic_id = 1 AND component_kind_code = 'RBC'`); n != 0 {
		t.Errorf("в прежнем Остатке %d, ожидали 0", n)
	}
}

// Событие с клиникой, расходящейся с копией, нарушает обязательство по копии: блокируется Остаток
// источника обязательства, резерв уменьшается там, где он был.
func TestWithdrawalEventWithWrongClinicStillBreaksByCopy(t *testing.T) {
	h := setup(t)
	units := h.registerUnits(1, "RBC", 1)
	o := h.open(1, "RBC", 1)
	h.confirm(o, units, false)
	h.aist.set(units[0], domain.UnitIssued)
	if err := h.svc.OnUnitWithdrawn(h.ctx, delivery(), app.UnitWithdrawn{
		UnitID: units[0], ClinicID: 2, ComponentKind: "RBC", Reason: "ISSUED", Version: h.versions.Add(1),
	}); err != nil {
		t.Fatal(err)
	}
	if s := h.view(o).Status; s != domain.StatusBroken {
		t.Fatalf("статус %s, ожидали BROKEN", s)
	}
	h.assertStockConsistent(1, "RBC")
}

// Событие по единице, которой нет в копии, ничего не меняет и не падает.
func TestStatusOfUnknownUnitIsIgnored(t *testing.T) {
	h := setup(t)
	if err := h.svc.OnUnitUpdated(h.ctx, delivery(), app.UnitUpdated{
		UnitID: uuid.New(), ClinicID: 1, ComponentKind: "RBC", Status: domain.UnitQuarantine, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if n := h.count(`SELECT count(*) FROM unit_copy`); n != 0 {
		t.Fatalf("в копии %d единиц", n)
	}
}
