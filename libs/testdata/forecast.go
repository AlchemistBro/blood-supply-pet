package testdata

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
)

// Генератор агрегированного прогноза ЧС для имитатора C02 (ИНТ-02, FR022). Прогноз синтетический
// (ADR-0010): сценарии, объёмы и доли придуманы для демонстрации механизма, а не взяты из медицинских
// данных. Сведений о пострадавших в прогнозе нет (NFR010): только вид компонента, группа, объём и,
// при наличии, принимающая клиника с долей.

// Коды сценариев ЧС (демо).
const (
	ScenarioRoadAccident        = "ROAD_ACCIDENT"
	ScenarioIndustrialExplosion = "INDUSTRIAL_EXPLOSION"
	ScenarioFlood               = "FLOOD"
)

// maxForecastScale — верхняя граница масштаба: больше — уже не демо, а нагрузочный тест другим средством.
const maxForecastScale = 100

// ForecastScenario — шаблон сценария ЧС.
type ForecastScenario struct {
	Code string
	Name string
	// Districts — сколько соседних районов затрагивает ЧС.
	Districts int
	// BaseVolumeML — суммарная потребность при масштабе 1.
	BaseVolumeML int64
	// HorizonHours — горизонт прогноза.
	HorizonHours int
	// KindMix — доли видов компонентов в потребности.
	KindMix map[string]float64
	// UndeterminedShare — доля потребности видов с группой, для которой группа не определена (FR040).
	UndeterminedShare float64
}

var scenarios = []ForecastScenario{
	{
		Code: ScenarioRoadAccident, Name: "ДТП с массовыми пострадавшими (демо)", Districts: 1,
		BaseVolumeML: 60_000, HorizonHours: 24, UndeterminedShare: 0.2,
		KindMix: map[string]float64{"RBC": 0.60, "FFP": 0.25, "PLT": 0.10, "CRYO": 0.05},
	},
	{
		Code: ScenarioIndustrialExplosion, Name: "Взрыв на производстве (демо)", Districts: 2,
		BaseVolumeML: 120_000, HorizonHours: 48, UndeterminedShare: 0.3,
		KindMix: map[string]float64{"RBC": 0.50, "FFP": 0.30, "PLT": 0.12, "CRYO": 0.08},
	},
	{
		Code: ScenarioFlood, Name: "Паводок (демо)", Districts: 3,
		BaseVolumeML: 40_000, HorizonHours: 72, UndeterminedShare: 0.1,
		KindMix: map[string]float64{"RBC": 0.55, "FFP": 0.25, "PLT": 0.15, "CRYO": 0.05},
	},
}

// ForecastScenarios возвращает шаблоны сценариев.
func ForecastScenarios() []ForecastScenario {
	return slices.Clone(scenarios)
}

// ForecastConfig — параметры генерации прогноза.
type ForecastConfig struct {
	Seed     int64
	Scenario string
	// Scale — множитель потребности сценария, (0, 100].
	Scale float64
	// WithReceivers — указать принимающие клиники с долями; иначе planning распределит по долям ФП2
	// с пометкой «грубая оценка» (FR024).
	WithReceivers bool
	// Now — момент формирования прогноза (из Clock); начало периода — час, в который он попадает.
	Now time.Time
	// Version — явная версия сценария; пусто — строится из сценария, времени, seed и варианта.
	Version string
}

// ForecastDistrict — район затронутой территории с весом (доля населения территории).
type ForecastDistrict struct {
	DistrictID   int64
	DistrictCode string
	Weight       float64
}

// ForecastTerritory — затронутая территория.
type ForecastTerritory struct {
	Name      string
	Districts []ForecastDistrict
}

// ForecastLine — строка прогноза: вид × группа (необязательно) × объём, принимающая клиника и её доля
// (необязательно).
type ForecastLine struct {
	ComponentKind string
	BloodGroup    string
	VolumeML      int64
	ClinicCode    string
	Share         float64
}

// Forecast — агрегированный прогноз ЧС.
type Forecast struct {
	ScenarioVersion string
	ScenarioKind    string
	ScenarioName    string
	Territory       ForecastTerritory
	PeriodStart     time.Time
	PeriodEnd       time.Time
	Lines           []ForecastLine
	// Demo всегда true: прогноз синтетический (ADR-0010).
	Demo bool
}

type receiver struct {
	code  string
	share float64
}

// GenerateForecast строит прогноз. Одинаковые seed, сценарий, масштаб, вариант и Now дают одинаковый прогноз.
func GenerateForecast(ref *refdata.Set, cfg ForecastConfig) (*Forecast, error) {
	idx := slices.IndexFunc(scenarios, func(s ForecastScenario) bool { return s.Code == cfg.Scenario })
	if idx < 0 {
		return nil, fmt.Errorf("неизвестный сценарий ЧС %q", cfg.Scenario)
	}
	sc := scenarios[idx]
	if !(cfg.Scale > 0 && cfg.Scale <= maxForecastScale) {
		return nil, fmt.Errorf("масштаб %v вне (0, %d]", cfg.Scale, maxForecastScale)
	}
	if cfg.Now.IsZero() {
		return nil, errors.New("не задан момент формирования прогноза")
	}
	if len(ref.Districts) == 0 {
		return nil, errors.New("справочник районов пуст")
	}
	rng := rand.New(rand.NewPCG(uint64(cfg.Seed), uint64(cfg.Seed)^0xC02C02C02C02C021)) //nolint:gosec // детерминированные демо-данные, секретов нет

	districts := slices.Clone(ref.Districts)
	slices.SortFunc(districts, func(a, b refdata.District) int { return cmp.Compare(a.DistrictID, b.DistrictID) })
	first := rng.IntN(len(districts))
	n := min(sc.Districts, len(districts))
	chosen := make([]refdata.District, 0, n)
	var population int64
	for i := 0; i < n; i++ {
		d := districts[(first+i)%len(districts)]
		chosen = append(chosen, d)
		population += d.Population
	}
	slices.SortFunc(chosen, func(a, b refdata.District) int { return cmp.Compare(a.DistrictID, b.DistrictID) })
	territory := ForecastTerritory{}
	names := make([]string, 0, n)
	for _, d := range chosen {
		territory.Districts = append(territory.Districts, ForecastDistrict{
			DistrictID: d.DistrictID, DistrictCode: d.DistrictCode,
			Weight: round4(float64(d.Population) / float64(population)),
		})
		names = append(names, d.DistrictName)
	}
	territory.Name = strings.Join(names, ", ")

	var receivers []receiver
	if cfg.WithReceivers {
		receivers = pickReceivers(rng, ref, chosen)
	}

	totalML := float64(sc.BaseVolumeML) * cfg.Scale * (0.85 + 0.3*rng.Float64())
	var lines []ForecastLine
	for _, k := range ref.ComponentKinds {
		kindML := totalML * sc.KindMix[k.Code]
		if kindML <= 0 {
			continue
		}
		type part struct {
			group  string
			volume float64
		}
		var parts []part
		if k.IsGroupApplicable {
			// Шум по группам ±20 % вокруг долей населения: прогноз не повторяет популяцию буквально.
			determined := kindML * (1 - sc.UndeterminedShare)
			for _, g := range ref.BloodGroups {
				parts = append(parts, part{group: g.Code, volume: determined * g.PopulationShare * (0.8 + 0.4*rng.Float64())})
			}
			parts = append(parts, part{volume: kindML * sc.UndeterminedShare})
		} else {
			parts = append(parts, part{volume: kindML})
		}
		for _, p := range parts {
			if len(receivers) == 0 {
				lines = appendLine(lines, ForecastLine{ComponentKind: k.Code, BloodGroup: p.group, VolumeML: round10(p.volume)})
				continue
			}
			for _, r := range receivers {
				lines = appendLine(lines, ForecastLine{ComponentKind: k.Code, BloodGroup: p.group,
					VolumeML: round10(p.volume * r.share), ClinicCode: r.code, Share: r.share})
			}
		}
	}

	start := cfg.Now.UTC().Truncate(time.Hour)
	version := cfg.Version
	if version == "" {
		variant := "n"
		if cfg.WithReceivers {
			variant = "r"
		}
		version = fmt.Sprintf("%s-%s-s%d-x%s-%s", strings.ToLower(strings.ReplaceAll(sc.Code, "_", "-")),
			cfg.Now.UTC().Format("20060102T1504"), cfg.Seed, strconv.FormatFloat(cfg.Scale, 'f', -1, 64), variant)
	}
	return &Forecast{
		ScenarioVersion: version, ScenarioKind: sc.Code, ScenarioName: sc.Name, Territory: territory,
		PeriodStart: start, PeriodEnd: start.Add(time.Duration(sc.HorizonHours) * time.Hour),
		Lines: lines, Demo: true,
	}, nil
}

// pickReceivers выбирает 2–4 действующие клиники территории и их доли (сумма 1).
func pickReceivers(rng *rand.Rand, ref *refdata.Set, chosen []refdata.District) []receiver {
	inTerritory := map[int64]bool{}
	for _, d := range chosen {
		inTerritory[d.DistrictID] = true
	}
	var candidates []refdata.Clinic
	for _, c := range ref.Clinics {
		if c.IsActive && inTerritory[c.DistrictID] {
			candidates = append(candidates, c)
		}
	}
	slices.SortFunc(candidates, func(a, b refdata.Clinic) int { return cmp.Compare(a.ClinicID, b.ClinicID) })
	if len(candidates) == 0 {
		return nil
	}
	k := min(len(candidates), 2+rng.IntN(3))
	perm := rng.Perm(len(candidates))[:k]
	slices.Sort(perm)
	weights := make([]float64, k)
	var sum float64
	for i := range weights {
		weights[i] = 0.5 + rng.Float64()
		sum += weights[i]
	}
	out := make([]receiver, k)
	var assigned float64
	for i, p := range perm {
		share := round4(weights[i] / sum)
		if i == k-1 {
			// Последней доле — остаток, чтобы сумма долей была ровно 1 с точностью до 4 знаков.
			share = round4(1 - assigned)
		}
		assigned += share
		out[i] = receiver{code: candidates[p].ClinicCode, share: share}
	}
	return out
}

func appendLine(lines []ForecastLine, l ForecastLine) []ForecastLine {
	if l.VolumeML <= 0 {
		return lines
	}
	return append(lines, l)
}

// round10 — объём строки до 10 мл: прогноз грубый, точность до миллилитра была бы ложной.
func round10(v float64) int64 { return int64(math.Round(v/10)) * 10 }

func round4(v float64) float64 { return math.Round(v*1e4) / 1e4 }
