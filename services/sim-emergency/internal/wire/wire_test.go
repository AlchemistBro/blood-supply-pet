package wire

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/testdata"
)

func sampleForecast() *testdata.Forecast {
	start := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	return &testdata.Forecast{
		ScenarioVersion: "flood-1", ScenarioKind: testdata.ScenarioFlood, ScenarioName: "Паводок (демо)",
		Territory: testdata.ForecastTerritory{Name: "Центральный", Districts: []testdata.ForecastDistrict{
			{DistrictID: 1, DistrictCode: "D01", Weight: 1},
		}},
		PeriodStart: start, PeriodEnd: start.Add(72 * time.Hour), Demo: true,
		Lines: []testdata.ForecastLine{
			{ComponentKind: "RBC", BloodGroup: "O+", VolumeML: 1200, ClinicCode: "C001", Share: 1},
			{ComponentKind: "CRYO", VolumeML: 40},
		},
	}
}

// keys собирает все имена полей JSON на любой глубине.
func keys(t *testing.T, body []byte) map[string]bool {
	t.Helper()
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("тело не JSON: %v", err)
	}
	out := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				out[k] = true
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return out
}

func TestBodyWithoutInjectHasNoPersonalData(t *testing.T) {
	body, err := Body(sampleForecast(), InjectNone)
	if err != nil {
		t.Fatal(err)
	}
	k := keys(t, body)
	for _, forbidden := range PIIFields {
		if k[forbidden] {
			t.Fatalf("в прогнозе без inject есть поле %q (NFR010)", forbidden)
		}
	}
	for _, required := range []string{"scenario_version", "territory", "period_start", "period_end", "lines",
		"component_kind", "volume_ml", "clinic_code", "share", "blood_group", "district_code", "weight", "demo"} {
		if !k[required] {
			t.Fatalf("в прогнозе нет поля %q: %s", required, body)
		}
	}
}

func TestBodyWithInjectPIIAddsForbiddenFields(t *testing.T) {
	body, err := Body(sampleForecast(), InjectPII)
	if err != nil {
		t.Fatal(err)
	}
	k := keys(t, body)
	for _, forbidden := range PIIFields {
		if !k[forbidden] {
			t.Fatalf("inject=pii не добавил поле %q: %s", forbidden, body)
		}
	}
	var top struct {
		Patients []map[string]any `json:"patients"`
		Lines    []map[string]any `json:"lines"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatal(err)
	}
	if len(top.Patients) == 0 || len(top.Lines) != 2 {
		t.Fatalf("patients=%d, lines=%d", len(top.Patients), len(top.Lines))
	}
}

func TestBodyIsDeterministic(t *testing.T) {
	for _, inject := range []Inject{InjectNone, InjectPII} {
		a, err := Body(sampleForecast(), inject)
		if err != nil {
			t.Fatal(err)
		}
		b, err := Body(sampleForecast(), inject)
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Fatalf("inject=%q: тела различаются", inject)
		}
	}
}

func TestParseInject(t *testing.T) {
	cases := []struct {
		in   string
		want Inject
		ok   bool
	}{
		{"", InjectNone, true},
		{"none", InjectNone, true},
		{"pii", InjectPII, true},
		{"PII", InjectPII, true},
		{"sql", "", false},
	}
	for _, tc := range cases {
		got, err := ParseInject(tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Fatalf("ParseInject(%q) = %q, %v", tc.in, got, err)
		}
	}
}
