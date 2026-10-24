package int02_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Белый список ИНТ-02 (NFR010): прогноз с полями о пострадавших — на верхнем уровне или в строке —
// схему не проходит. Сведения о пострадавших в системе не принимаются и не хранятся.
func TestForecastSchemaRejectsPersonalData(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile("../schemas/int02/EmergencyForecast.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	load := func() map[string]any {
		raw, err := os.ReadFile("../examples/int02/EmergencyForecast.v1.json")
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if err := schema.Validate(load()); err != nil {
		t.Fatalf("чистый пример должен проходить: %v", err)
	}

	cases := map[string]func(v map[string]any){
		"список пациентов": func(v map[string]any) {
			v["patients"] = []any{map[string]any{"full_name": "Иванов Иван", "snils": "000-000-000 00"}}
		},
		"СНИЛС на верхнем уровне": func(v map[string]any) { v["snils"] = "000-000-000 00" },
		"ФИО в строке прогноза": func(v map[string]any) {
			lines := v["lines"].([]any)
			lines[0].(map[string]any)["full_name"] = "Иванов Иван"
		},
		"номер случая в строке": func(v map[string]any) {
			lines := v["lines"].([]any)
			lines[0].(map[string]any)["patient_case_no"] = "2026/123"
		},
	}
	for name, mutate := range cases {
		v := load()
		mutate(v)
		if err := schema.Validate(v); err == nil {
			t.Errorf("%s: прогноз с ПДн прошёл схему", name)
		}
	}
}
