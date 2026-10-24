// Package testdata — детерминированный генератор синтетических данных (ADR-0010):
// единицы компонентов крови по клиникам с реалистичным распределением групп и сроков годности,
// годовой расход для норматива. Один сид → один и тот же набор. Используется имитатором АИСТ-М,
// бенчмарком NFR002 и e2e-тестами.
package testdata

import (
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
)

// Unit — сгенерированная единица в терминах АИСТ-М.
type Unit struct {
	AistUnitCode    string
	ClinicCode      string
	ComponentKind   string
	BloodGroup      string
	Phenotype       string
	VolumeML        int64
	ExpiresAt       time.Time
	Status          string
	StorageLocation string
}

// AnnualConsumption — годовой расход клиники по виду (вход норматива, FR012).
type AnnualConsumption struct {
	ClinicCode    string
	ComponentKind string
	Year          int
	VolumeML      int64
}

// Config — параметры генерации.
type Config struct {
	Seed int64
	// Units — общее число единиц; распределяется по клиникам пропорционально населению района
	// (центр крови получает в 5 раз больше).
	Units int
	// Now — точка отсчёта сроков годности.
	Now time.Time
	// WithdrawnShare — доля единиц в состояниях, недоступных для передачи.
	WithdrawnShare float64
}

// DefaultConfig — 100 клиник, 100 000 единиц: размер из NFR002.
func DefaultConfig() Config {
	return Config{Seed: 20261020, Units: 100_000, Now: time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC), WithdrawnShare: 0.05}
}

// Dataset — результат генерации.
type Dataset struct {
	Units       []Unit
	Consumption []AnnualConsumption
}

var kindShares = map[string]float64{"RBC": 0.55, "FFP": 0.25, "PLT": 0.12, "CRYO": 0.08}

var statuses = []string{"IN_STOCK", "QUARANTINE", "ISSUED", "DISCARDED", "TRANSFERRED"}

// Generate строит набор.
func Generate(ref *refdata.Set, cfg Config) *Dataset {
	rng := rand.New(rand.NewPCG(uint64(cfg.Seed), uint64(cfg.Seed)^0x9E3779B97F4A7C15)) //nolint:gosec // детерминированные демо-данные, секретов нет
	weights := clinicWeights(ref)
	kinds := make([]string, 0, len(ref.ComponentKinds))
	kindW := make([]float64, 0, len(ref.ComponentKinds))
	for _, k := range ref.ComponentKinds {
		kinds = append(kinds, k.Code)
		kindW = append(kindW, kindShares[k.Code])
	}
	groups := make([]string, 0, len(ref.BloodGroups))
	groupW := make([]float64, 0, len(ref.BloodGroups))
	for _, g := range ref.BloodGroups {
		groups = append(groups, g.Code)
		groupW = append(groupW, g.PopulationShare)
	}
	ds := &Dataset{Units: make([]Unit, 0, cfg.Units)}
	for i := 0; i < cfg.Units; i++ {
		clinic := ref.Clinics[pick(rng, weights)]
		kindCode := kinds[pick(rng, kindW)]
		kind, _ := ref.Kind(kindCode)
		// Остаток срока годности: равномерно по сроку хранения, с перекосом к свежим.
		remaining := time.Duration(float64(kind.ShelfLifeDays*24) * (1 - rng.Float64()*rng.Float64()) * float64(time.Hour))
		status := "IN_STOCK"
		if rng.Float64() < cfg.WithdrawnShare {
			status = statuses[1+rng.IntN(len(statuses)-1)]
		}
		volume := kind.TypicalVolumeML + int64(rng.IntN(int(kind.TypicalVolumeML/5+1))) - kind.TypicalVolumeML/10
		ds.Units = append(ds.Units, Unit{
			AistUnitCode:    fmt.Sprintf("AIST-%s-%07d", clinic.ClinicCode, i+1),
			ClinicCode:      clinic.ClinicCode,
			ComponentKind:   kindCode,
			BloodGroup:      groups[pick(rng, groupW)],
			Phenotype:       phenotype(rng),
			VolumeML:        volume,
			ExpiresAt:       cfg.Now.Add(remaining).Truncate(time.Hour),
			Status:          status,
			StorageLocation: fmt.Sprintf("Холодильник %d, полка %d", 1+rng.IntN(6), 1+rng.IntN(4)),
		})
	}
	// Годовой расход: пропорционален доле клиники в наборе, 12 единиц объёма в год на единицу запаса.
	perClinicKind := map[string]int64{}
	for _, u := range ds.Units {
		perClinicKind[u.ClinicCode+"|"+u.ComponentKind] += u.VolumeML
	}
	for _, c := range ref.Clinics {
		for _, k := range ref.ComponentKinds {
			stock := perClinicKind[c.ClinicCode+"|"+k.Code]
			if stock == 0 {
				continue
			}
			ds.Consumption = append(ds.Consumption, AnnualConsumption{
				ClinicCode: c.ClinicCode, ComponentKind: k.Code, Year: cfg.Now.Year() - 1,
				VolumeML: stock * 12 * (80 + int64(rng.IntN(41))) / 100,
			})
		}
	}
	return ds
}

func clinicWeights(ref *refdata.Set) []float64 {
	pop := map[int64]int64{}
	count := map[int64]int{}
	for _, d := range ref.Districts {
		pop[d.DistrictID] = d.Population
	}
	for _, c := range ref.Clinics {
		count[c.DistrictID]++
	}
	w := make([]float64, len(ref.Clinics))
	for i, c := range ref.Clinics {
		w[i] = float64(pop[c.DistrictID]) / float64(count[c.DistrictID])
		if c.IsBloodCenter {
			w[i] *= 5
		}
	}
	return w
}

func pick(rng *rand.Rand, weights []float64) int {
	var total float64
	for _, w := range weights {
		total += w
	}
	x := rng.Float64() * total
	for i, w := range weights {
		x -= w
		if x <= 0 {
			return i
		}
	}
	return len(weights) - 1
}

// phenotype — упрощённый синтетический фенотип: пусто в 70 % случаев.
func phenotype(rng *rand.Rand) string {
	if rng.Float64() < 0.7 {
		return ""
	}
	markers := []string{"C", "c", "E", "e", "K"}
	out := ""
	for _, m := range markers {
		if rng.IntN(2) == 0 {
			out += m + "+"
		} else {
			out += m + "-"
		}
	}
	return out
}
