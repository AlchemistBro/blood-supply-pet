package contracts_test

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
)

// Коды видов компонента и групп крови в схемах совпадают со справочниками libs/refdata: иначе схема
// пропустит вид, которого нет в базе получателя, или отвергнет настоящий.
func TestSchemaDictionaryEnumsMatchRefdata(t *testing.T) {
	ref := refdata.MustLoad()
	kinds := make([]string, 0, len(ref.ComponentKinds))
	for _, k := range ref.ComponentKinds {
		kinds = append(kinds, k.Code)
	}
	groups := make([]string, 0, len(ref.BloodGroups))
	for _, g := range ref.BloodGroups {
		groups = append(groups, g.Code)
	}
	want := map[string][]string{"component_kind": kinds, "blood_group": groups}

	schemas, err := filepath.Glob("schemas/*/*.json")
	if err != nil || len(schemas) == 0 {
		t.Fatalf("схемы не найдены: %v", err)
	}
	found := map[string]int{}
	for _, path := range schemas {
		forEachEnumProperty(readJSON(t, path), "", func(name, at string, enum []string) {
			expected, ok := want[name]
			if !ok {
				return
			}
			found[name]++
			if !sameSet(enum, expected) {
				t.Errorf("%s: %s%s = %v, в справочнике %v", path, at, name, enum, expected)
			}
		})
	}
	if found["component_kind"] == 0 {
		t.Fatal("ни одной схемы с component_kind: обход не работает")
	}
}

// Go-константы причин и статусов совпадают с перечислениями схем, где они объявлены.
func TestGoEnumsMatchSchemas(t *testing.T) {
	cases := []struct {
		schema, property string
		values           []string
	}{
		{"transfer/OpenObligation", "origin", transfer.Origins},
		{"transfer/OpenObligation", "urgency", transfer.Urgencies},
		{"transfer/ObligationOpened", "origin", transfer.Origins},
		{"transfer/CancelObligation", "reason", transfer.CancelReasons},
		{"transfer/TransferCancelled", "reason", transfer.CancelReasons},
		{"transfer/TransferRejected", "reason", transfer.RejectReasons},
		{"transfer/TransferBroken", "reason", transfer.BrokenReasons},
		{"transfer/TransportRequestFailed", "reason", logistics.RejectReasons},
		{"transfer/EscalationRaised", "reason", transfer.EscalationReasons},
		{"logistics/RequestTransport", "temperature_regime", logistics.Regimes},
		{"logistics/CancelTransport", "reason", logistics.CancelReasons},
		{"logistics/TransportRejected", "reason", logistics.RejectReasons},
		{"inventory/UnitRegistered", "status", inventory.Statuses},
		{"inventory/UnitUpdated", "status", inventory.UpdatedStatuses},
		{"inventory/UnitWithdrawn", "reason", inventory.WithdrawReasons},
		{"inventory/SyncRunCompleted", "kind", inventory.SyncKinds},
		{"inventory/SyncRunCompleted", "result", inventory.SyncResults},
	}
	for _, c := range cases {
		schema := readJSON(t, "schemas/"+c.schema+".v1.json")
		props, _ := schema["properties"].(map[string]any)
		prop, _ := props[c.property].(map[string]any)
		enum := toStrings(prop["enum"])
		if len(enum) == 0 {
			t.Errorf("%s.%s: в схеме нет перечисления", c.schema, c.property)
			continue
		}
		if !sameSet(enum, c.values) {
			t.Errorf("%s.%s: в схеме %v, в Go %v", c.schema, c.property, enum, c.values)
		}
	}
}

// forEachEnumProperty обходит все свойства с перечислением на любом уровне схемы (включая items массивов).
func forEachEnumProperty(node map[string]any, at string, fn func(name, at string, enum []string)) {
	if props, ok := node["properties"].(map[string]any); ok {
		for name, raw := range props {
			prop, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if enum := toStrings(prop["enum"]); len(enum) > 0 {
				fn(name, at, enum)
			}
			forEachEnumProperty(prop, at+name+".", fn)
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		forEachEnumProperty(items, at+"[].", fn)
	}
}

func toStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func sameSet(a, b []string) bool {
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	return strings.Join(x, ",") == strings.Join(y, ",")
}
