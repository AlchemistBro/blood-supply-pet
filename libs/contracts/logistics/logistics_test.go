package logistics_test

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
)

// Для каждого вида компонента из справочника известен температурный режим перевозки:
// без него команду перевозчику не собрать (ПП РФ № 641, п. 68 в — перевозка раздельно по температурным режимам).
func TestEveryComponentKindHasTemperatureRegime(t *testing.T) {
	allowed := map[string]bool{
		logistics.RegimeCold2to6: true, logistics.RegimeFrozenMinus25: true, logistics.RegimeRoom20to24: true,
	}
	for _, k := range refdata.MustLoad().ComponentKinds {
		regime, ok := logistics.TemperatureRegimeFor(k.Code)
		if !ok || !allowed[regime] {
			t.Errorf("вид %s: режим %q, найден=%v", k.Code, regime, ok)
		}
	}
	if _, ok := logistics.TemperatureRegimeFor("UNKNOWN"); ok {
		t.Error("неизвестный вид не должен получать режим по умолчанию")
	}
}

// Причины снятия заказа в Go и в схеме совпадают; карантин закреплённой единицы снимает заказ (ADR-0027).
func TestCancelReasonsMatchSchema(t *testing.T) {
	raw, err := os.ReadFile("../schemas/logistics/CancelTransport.v1.json")
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
	want := append([]string(nil), logistics.CancelReasons...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("в схеме %v, в Go %v", got, want)
	}
	if !strings.Contains(","+strings.Join(want, ",")+",", ","+logistics.CancelUnitQuarantined+",") {
		t.Fatal("карантин закреплённой единицы должен снимать заказ перевозки")
	}
}

// Идентификатор работника службы перевозки — служебный код без пробелов (ПП РФ № 641, п. 73; ADR-0022):
// ФИО шаблон не пропускает.
func TestCourierRefIsServiceIdentifier(t *testing.T) {
	re := regexp.MustCompile(logistics.CourierRefPattern)
	for _, ok := range []string{"courier-0042", "c03:tab.1187"} {
		if !re.MatchString(ok) {
			t.Errorf("служебный идентификатор %q отвергнут", ok)
		}
	}
	for _, bad := range []string{"Иванов Иван Иванович", "ivanov ivan", ""} {
		if re.MatchString(bad) {
			t.Errorf("значение %q прошло шаблон служебного идентификатора", bad)
		}
	}
	for _, path := range []string{"../schemas/logistics/TransportDispatched.v1.json", "../schemas/logistics/TransportDelivered.v1.json"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]struct {
				Pattern string `json:"pattern"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		if got := schema.Properties["courier_ref"].Pattern; got != logistics.CourierRefPattern {
			t.Errorf("%s: courier_ref с шаблоном %q, ожидали %q", path, got, logistics.CourierRefPattern)
		}
	}
}

func TestDemoRegimesByKind(t *testing.T) {
	cases := map[string]string{
		"RBC":  logistics.RegimeCold2to6,
		"FFP":  logistics.RegimeFrozenMinus25,
		"CRYO": logistics.RegimeFrozenMinus25,
		"PLT":  logistics.RegimeRoom20to24,
	}
	for kind, want := range cases {
		if got, _ := logistics.TemperatureRegimeFor(kind); got != want {
			t.Errorf("%s: режим %s, ожидали %s", kind, got, want)
		}
	}
}
