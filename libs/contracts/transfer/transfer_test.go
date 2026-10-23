package transfer_test

import (
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
)

// Причины нарушения обязательства в Go и в схеме совпадают. Карантин закреплённой единицы (статус по
// ПП РФ № 641, п. 62 а) — тоже нарушение по решению проекта (ADR-0027), хотя единица не выбыла: поэтому у TransferBroken своё перечисление,
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

// Первые четыре причины нарушения повторяют причины выбытия UnitWithdrawn: transfer передаёт причину выбытия
// дальше как есть, и коды не должны разойтись молча.
func TestBrokenReasonsRepeatWithdrawReasons(t *testing.T) {
	pairs := map[string]string{
		transfer.BrokenIssued:      inventory.WithdrawIssued,
		transfer.BrokenDiscarded:   inventory.WithdrawDiscarded,
		transfer.BrokenTransferred: inventory.WithdrawTransferred,
		transfer.BrokenExpired:     inventory.WithdrawExpired,
	}
	for broken, withdrawn := range pairs {
		if broken != withdrawn {
			t.Errorf("причина нарушения %s не совпадает с причиной выбытия %s", broken, withdrawn)
		}
	}
	if len(inventory.WithdrawReasons) != len(pairs) {
		t.Errorf("причин выбытия %d, а парных причин нарушения %d: новую причину выбытия надо добавить и в TransferBroken",
			len(inventory.WithdrawReasons), len(pairs))
	}
	for _, w := range inventory.WithdrawReasons {
		ok := false
		for _, b := range transfer.BrokenReasons {
			ok = ok || b == w
		}
		if !ok {
			t.Errorf("причины выбытия %s нет среди причин нарушения", w)
		}
	}
}
