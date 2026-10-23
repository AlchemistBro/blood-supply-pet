package testdata

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
)

var forecastNow = time.Date(2026, 10, 23, 9, 17, 0, 0, time.UTC)

func forecastCfg(seed int64) ForecastConfig {
	return ForecastConfig{Seed: seed, Scenario: ScenarioRoadAccident, Scale: 1, WithReceivers: true, Now: forecastNow}
}

func TestForecastIsDeterministicBySeed(t *testing.T) {
	ref := refdata.MustLoad()
	for _, sc := range ForecastScenarios() {
		cfg := forecastCfg(42)
		cfg.Scenario = sc.Code
		a, err := GenerateForecast(ref, cfg)
		if err != nil {
			t.Fatalf("%s: %v", sc.Code, err)
		}
		b, err := GenerateForecast(ref, cfg)
		if err != nil {
			t.Fatalf("%s: %v", sc.Code, err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%s: один seed дал разные прогнозы", sc.Code)
		}
		cfg.Seed = 43
		c, err := GenerateForecast(ref, cfg)
		if err != nil {
			t.Fatalf("%s: %v", sc.Code, err)
		}
		if reflect.DeepEqual(a.Lines, c.Lines) {
			t.Fatalf("%s: разные seed дали одинаковые строки", sc.Code)
		}
		if a.ScenarioVersion == c.ScenarioVersion {
			t.Fatalf("%s: версия сценария не зависит от seed: %s", sc.Code, a.ScenarioVersion)
		}
	}
}

func TestForecastShape(t *testing.T) {
	ref := refdata.MustLoad()
	cases := []struct {
		name      string
		receivers bool
	}{
		{"с принимающими клиниками", true},
		{"без принимающих клиник - распределение по долям ФП2", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := forecastCfg(7)
			cfg.WithReceivers = tc.receivers
			f, err := GenerateForecast(ref, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if !f.Demo || f.ScenarioVersion == "" || f.ScenarioKind != ScenarioRoadAccident {
				t.Fatalf("заголовок прогноза: %+v", f)
			}
			if !f.PeriodStart.Equal(forecastNow.Truncate(time.Hour)) || !f.PeriodEnd.After(f.PeriodStart) {
				t.Fatalf("период %s - %s", f.PeriodStart, f.PeriodEnd)
			}
			inTerritory := map[int64]bool{}
			var weights float64
			for _, d := range f.Territory.Districts {
				inTerritory[d.DistrictID] = true
				weights += d.Weight
			}
			if len(inTerritory) == 0 || math.Abs(weights-1) > 1e-3 {
				t.Fatalf("территория %+v, сумма весов %.4f", f.Territory, weights)
			}
			kinds := map[string]refdata.ComponentKind{}
			for _, k := range ref.ComponentKinds {
				kinds[k.Code] = k
			}
			groups := map[string]bool{}
			for _, g := range ref.BloodGroups {
				groups[g.Code] = true
			}
			if len(f.Lines) == 0 {
				t.Fatal("прогноз без строк")
			}
			for _, l := range f.Lines {
				k, ok := kinds[l.ComponentKind]
				if !ok || l.VolumeML <= 0 {
					t.Fatalf("строка %+v: неизвестный вид или пустой объём", l)
				}
				if l.BloodGroup != "" && (!k.IsGroupApplicable || !groups[l.BloodGroup]) {
					t.Fatalf("строка %+v: группа у вида без группы или неизвестная группа", l)
				}
				if tc.receivers != (l.ClinicCode != "") {
					t.Fatalf("строка %+v: принимающая клиника при receivers=%v", l, tc.receivers)
				}
				if l.ClinicCode != "" {
					c, ok := clinicByCode(ref, l.ClinicCode)
					if !ok || !inTerritory[c.DistrictID] || l.Share <= 0 || l.Share > 1 {
						t.Fatalf("строка %+v: клиника вне территории или доля вне (0, 1]", l)
					}
				}
			}
		})
	}
}

func TestForecastScaleAndValidation(t *testing.T) {
	ref := refdata.MustLoad()
	small, err := GenerateForecast(ref, forecastCfg(1))
	if err != nil {
		t.Fatal(err)
	}
	cfg := forecastCfg(1)
	cfg.Scale = 3
	big, err := GenerateForecast(ref, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if total(big) < 2*total(small) {
		t.Fatalf("масштаб 3 дал %d мл против %d мл", total(big), total(small))
	}
	if small.ScenarioVersion == big.ScenarioVersion {
		t.Fatal("версия сценария не зависит от масштаба")
	}
	for _, bad := range []ForecastConfig{
		{Seed: 1, Scenario: "UNKNOWN", Scale: 1, Now: forecastNow},
		{Seed: 1, Scenario: ScenarioFlood, Scale: 0, Now: forecastNow},
		{Seed: 1, Scenario: ScenarioFlood, Scale: 1000, Now: forecastNow},
		{Seed: 1, Scenario: ScenarioFlood, Scale: 1},
	} {
		if _, err := GenerateForecast(ref, bad); err == nil {
			t.Fatalf("конфигурация %+v принята", bad)
		}
	}
	cfg = forecastCfg(1)
	cfg.Version = "manual-1"
	f, err := GenerateForecast(ref, cfg)
	if err != nil || f.ScenarioVersion != "manual-1" {
		t.Fatalf("явная версия не применена: %v %v", f, err)
	}
}

func total(f *Forecast) int64 {
	var s int64
	for _, l := range f.Lines {
		s += l.VolumeML
	}
	return s
}

func clinicByCode(ref *refdata.Set, code string) (refdata.Clinic, bool) {
	for _, c := range ref.Clinics {
		if c.ClinicCode == code {
			return c, true
		}
	}
	return refdata.Clinic{}, false
}
