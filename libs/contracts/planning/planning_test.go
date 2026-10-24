package planning_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/planning"
)

// Защищённый объём уходит в единицах с округлением вверх (ADR-0019): иначе защищённый запас окажется
// меньше расчётного и подтверждение сможет его нарушить (NFR013).
func TestUnitsForVolumeRoundsUp(t *testing.T) {
	cases := []struct{ ml, typical, want int64 }{
		{0, 300, 0},
		{1, 300, 1},
		{300, 300, 1},
		{301, 300, 2},
		{1650, 300, 6},
		{500, 0, 0},
	}
	for _, c := range cases {
		if got := planning.UnitsForVolume(c.ml, c.typical); got != c.want {
			t.Errorf("UnitsForVolume(%d, %d) = %d, ожидали %d", c.ml, c.typical, got, c.want)
		}
	}
}

// Единица измерения записана в самой схеме: получатель не должен гадать, мл это или единицы.
func TestProtectedVolumeSchemaStatesUnits(t *testing.T) {
	raw, err := os.ReadFile("../schemas/planning/ProtectedVolumeCalculated.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Description string `json:"description"`
		Properties  map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	units := schema.Properties["volume_units"]
	if units.Type != "integer" || !strings.Contains(units.Description, "единиц") || !strings.Contains(units.Description, "ADR-0019") {
		t.Fatalf("volume_units должен быть целым в единицах со ссылкой на ADR-0019: %+v", units)
	}
	if !strings.Contains(schema.Description, "ЕДИНИЦАХ") {
		t.Fatalf("описание схемы не называет единицу измерения: %q", schema.Description)
	}
}
