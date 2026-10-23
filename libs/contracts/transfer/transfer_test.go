package transfer_test

import (
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
)

// Причины нарушения обязательства в Go и в схеме совпадают. Карантин закреплённой единицы — тоже нарушение
// (ПП РФ № 641, п. 62 а, ADR-0027), хотя единица не выбыла: поэтому у TransferBroken своё перечисление,
// шире, чем причины UnitWithdrawn.
func TestBrokenReasonsMatchSchema(t *testing.T) {
	raw, err := os.ReadFile("../schemas/transfer/TransferBroken.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	got := append([]string(nil), schema.Properties["reason"].Enum...)
	want := append([]string(nil), transfer.BrokenReasons...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("в схеме %v, в Go %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("в схеме %v, в Go %v", got, want)
		}
	}
	found := false
	for _, r := range transfer.BrokenReasons {
		if r == transfer.BrokenQuarantine {
			found = true
		}
	}
	if !found {
		t.Fatal("карантин закреплённой единицы должен быть причиной нарушения")
	}
}
