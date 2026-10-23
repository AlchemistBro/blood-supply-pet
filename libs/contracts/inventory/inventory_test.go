package inventory_test

import (
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
)

// Статусы в контракте и в справочнике ref_unit_status совпадают: иначе получатель события
// не найдёт признак доступности для пришедшего статуса.
func TestStatusesMatchRefdata(t *testing.T) {
	ref := refdata.MustLoad()
	if len(ref.UnitStatuses) != len(inventory.Statuses) {
		t.Fatalf("в справочнике %d статусов, в контракте %d", len(ref.UnitStatuses), len(inventory.Statuses))
	}
	for _, s := range inventory.Statuses {
		if _, ok := ref.UnitStatus(s); !ok {
			t.Errorf("статуса %s нет в справочнике", s)
		}
	}
	if !ref.IsUnitAvailable(inventory.StatusInStock) || ref.IsUnitAvailable(inventory.StatusQuarantine) {
		t.Error("доступна должна быть только единица IN_STOCK")
	}
}
