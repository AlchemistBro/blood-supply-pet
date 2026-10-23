//go:build integration

package app_test

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

// NFR004 без гонки: единственное, что не пускает единицу во второе обязательство, — частичный уникальный
// индекс allocation_one_confirmed_per_unit. Остаток больше единицы и защищённый объём 0, чтобы раньше
// не сработала проверка NFR013. При снятии индекса оба теста падают.

func TestUnitConfirmedInOneObligationIsTakenForAnother(t *testing.T) {
	h := setup(t)
	units := h.registerUnits(1, "RBC", 3)
	x := units[0]
	a := h.open(1, "RBC", 1)
	b := h.open(1, "RBC", 1)
	if res := h.confirm(a, []uuid.UUID{x}, false); !isType[domain.Confirmed](res) {
		t.Fatalf("подтверждение A: %#v", res)
	}

	res := h.confirm(b, []uuid.UUID{x}, false)
	if !reflect.DeepEqual(res, domain.UnitsTaken{Units: []uuid.UUID{x}}) {
		t.Fatalf("та же единица в B: ожидали UnitsTaken([X]), получили %#v", res)
	}
	if s := h.view(b).Status; s != domain.StatusPending {
		t.Fatalf("B после отказа: %s, ожидали PENDING (транзакция откатилась целиком)", s)
	}
	h.expectTypes("события B", h.eventTypes(b), "ObligationOpened")
	if n := h.count(`SELECT count(*) FROM allocation WHERE unit_id = $1 AND status = 'CONFIRMED'`, x); n != 1 {
		t.Fatalf("действующих закреплений X: %d", n)
	}
	h.assertStockConsistent(1, "RBC")
}

func TestReplacementWithUnitHeldByAnotherObligationIsTaken(t *testing.T) {
	h := setup(t)
	units := h.registerUnits(1, "RBC", 3)
	x, y := units[0], units[1]
	a := h.open(1, "RBC", 1)
	b := h.open(1, "RBC", 1)
	h.confirm(a, []uuid.UUID{x}, false)
	h.confirm(b, []uuid.UUID{y}, false)

	res := h.replace(b, y, x)
	if !reflect.DeepEqual(res, domain.UnitTaken{UnitID: x}) {
		t.Fatalf("замена на единицу A: ожидали UnitTaken(X), получили %#v", res)
	}
	if v := h.view(b); !equal(v.Units, []uuid.UUID{y}) {
		t.Fatalf("B после отказа держит %v, ожидали прежнюю Y", v.Units)
	}
	h.expectTypes("события B", h.eventTypes(b), "ObligationOpened", "TransferConfirmed")
	h.assertStockConsistent(1, "RBC")
}
