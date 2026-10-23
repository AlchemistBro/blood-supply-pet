package logistics_test

import (
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
)

// Для каждого вида компонента из справочника известен температурный режим перевозки:
// без него команду перевозчику не собрать (ПП РФ № 641, условия транспортировки).
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
