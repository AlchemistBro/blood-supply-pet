package testdata

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
)

// Генератор CSV территориального аналитика (формат — docs/ui/csv-formats.md): корректные файлы трёх видов
// и те же файлы с битыми строками. Данные синтетические (ADR-0010), один сид — один и тот же файл.

// CSVFile — сгенерированный файл и номера строк (заголовок — строка 1), которые проверка обязана отклонить.
type CSVFile struct {
	Name    string
	Content []byte
	Rows    int
	BadRows []int
}

// CSVConfig — параметры генерации.
type CSVConfig struct {
	Seed int64
	// Year — год актуальности профиля и долей; история расхода — помесячно за предыдущий год.
	Year int
	// Broken — дописать в конец битые строки (их номера — в CSVFile.BadRows).
	Broken bool
}

// DefaultCSVConfig — корректные файлы на 2026 год.
func DefaultCSVConfig() CSVConfig {
	return CSVConfig{Seed: 20261020, Year: 2026}
}

// Заголовки — контракт формата; меняются только вместе с docs/ui/csv-formats.md.
const (
	historyCSVHeader = "component_kind_code;period_start;period_end;consumed_volume_ml;data_source"
	shareCSVHeader   = "district_code;clinic_code;clinic_share;basis;data_source;period_start;period_end"
	demoSource       = "ДЕМО: синтетические данные (ADR-0010)"
	demoBasis        = "ДЕМО: схема маршрутизации"
)

// Средний суточный расход вида по городу, мл — демо-норматив (ADR-0010).
var dailyConsumptionML = map[string]int64{"RBC": 10_000, "FFP": 5_000, "PLT": 2_000, "CRYO": 500}

type csvBuilder struct {
	lines []string
	bad   []int
}

func (b *csvBuilder) add(line string) { b.lines = append(b.lines, line) }
func (b *csvBuilder) addBad(line string) {
	b.lines = append(b.lines, line)
	b.bad = append(b.bad, len(b.lines))
}

func (b *csvBuilder) file(name string) CSVFile {
	return CSVFile{Name: name, Content: []byte(strings.Join(b.lines, "\n") + "\n"), Rows: len(b.lines) - 1, BadRows: b.bad}
}

func newRand(seed int64, salt uint64) *rand.Rand {
	return rand.New(rand.NewPCG(uint64(seed), uint64(seed)^salt))
}

// shareText — доля из десятитысячных в записи с запятой, как у аналитика в Excel: 3300 → «0,33».
func shareText(units int64) string {
	s := fmt.Sprintf("%d,%04d", units/10000, units%10000)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ",")
}

func yearPeriod(year int) (string, string) {
	return fmt.Sprintf("%d-01-01", year), fmt.Sprintf("%d-12-31", year)
}

// DistrictProfileCSV — профиль всех районов справочника: население из справочника, доли групп — доли
// справочника с небольшим сдвигом между O+ и A+ по району; сумма долей ровно 1.
func DistrictProfileCSV(ref *refdata.Set, cfg CSVConfig) CSVFile {
	rng := newRand(cfg.Seed, 0x1F0)
	header := []string{"district_code", "population"}
	for _, g := range ref.BloodGroups {
		header = append(header, "share_"+g.Code)
	}
	header = append(header, "data_source", "period_start", "period_end")
	b := &csvBuilder{}
	b.add(strings.Join(header, ";"))
	start, end := yearPeriod(cfg.Year)
	for _, d := range ref.Districts {
		units := make([]int64, len(ref.BloodGroups))
		iPos, aPos := -1, -1
		var sum int64
		for i, g := range ref.BloodGroups {
			units[i] = int64(g.PopulationShare*10000 + 0.5)
			sum += units[i]
			switch g.Code {
			case "O+":
				iPos = i
			case "A+":
				aPos = i
			}
		}
		units[0] += 10000 - sum // справочник уже даёт 1; страховка от округления
		if iPos >= 0 && aPos >= 0 {
			shift := int64(rng.IntN(101)) - 50
			if units[aPos]-shift >= 0 && units[iPos]+shift >= 0 {
				units[iPos] += shift
				units[aPos] -= shift
			}
		}
		row := []string{d.DistrictCode, fmt.Sprintf("%d", d.Population)}
		for _, u := range units {
			row = append(row, shareText(u))
		}
		row = append(row, demoSource, start, end)
		b.add(strings.Join(row, ";"))
	}
	if cfg.Broken {
		zeros := strings.TrimSuffix(strings.Repeat("0;", len(ref.BloodGroups)), ";")
		first := ref.Districts[0].DistrictCode
		b.addBad("D99;1000;1" + zeros[1:] + ";" + demoSource + ";" + start + ";" + end) // неизвестный район
		over := "1,2" + zeros[1:]
		b.addBad("D98X;1000;" + over + ";" + demoSource + ";" + start + ";" + end) // неизвестный район и сумма 1,2
		b.addBad(first + ";1000;1" + zeros[1:] + ";;" + start + ";" + end)         // повтор района и нет источника
		b.addBad(first + ";abc;1" + zeros[1:] + ";" + demoSource + ";;")           // население не число, нет периода
	}
	return b.file(fmt.Sprintf("district_profile_%d.csv", cfg.Year))
}

// ConsumptionHistoryCSV — расход каждого активного вида по месяцам предыдущего года: суточный демо-расход
// × дни месяца с разбросом ±10 %.
func ConsumptionHistoryCSV(ref *refdata.Set, cfg CSVConfig) CSVFile {
	rng := newRand(cfg.Seed, 0x2F0)
	b := &csvBuilder{}
	b.add(historyCSVHeader)
	year := cfg.Year - 1
	var firstKind string
	for _, k := range ref.ComponentKinds {
		if !k.IsActive {
			continue
		}
		if firstKind == "" {
			firstKind = k.Code
		}
		daily := dailyConsumptionML[k.Code]
		if daily == 0 {
			daily = 1_000
		}
		for m := time.January; m <= time.December; m++ {
			start := time.Date(year, m, 1, 0, 0, 0, 0, time.UTC)
			end := start.AddDate(0, 1, -1)
			days := int64(end.Day())
			volume := daily * days * int64(90+rng.IntN(21)) / 100
			b.add(fmt.Sprintf("%s;%s;%s;%d;%s", k.Code, start.Format(time.DateOnly), end.Format(time.DateOnly), volume, demoSource))
		}
	}
	if cfg.Broken {
		b.addBad(fmt.Sprintf(";%d-01-01;%d-01-31;100;%s", year, year, demoSource))              // нет вида
		b.addBad(fmt.Sprintf("%s;;;100;%s", firstKind, demoSource))                             // нет периода
		b.addBad(fmt.Sprintf("%s;%d-01-01;%d-01-31;100,5;%s", "XYZ", year, year, demoSource))   // неизвестный вид, дробный объём
		b.addBad(fmt.Sprintf("%s;%d-01-15;%d-02-15;100;%s", firstKind, year, year, demoSource)) // пересекается с январём
	}
	return b.file(fmt.Sprintf("consumption_history_%d.csv", year))
}

// ClinicShareCSV — доли район → клиника: каждый район делится между своими активными клиниками со
// случайными весами; район без клиник целиком относится к центру крови. Сумма долей района ровно 1.
func ClinicShareCSV(ref *refdata.Set, cfg CSVConfig) CSVFile {
	rng := newRand(cfg.Seed, 0x3F0)
	byDistrict := map[int64][]refdata.Clinic{}
	var center *refdata.Clinic
	for i, c := range ref.Clinics {
		if !c.IsActive {
			continue
		}
		byDistrict[c.DistrictID] = append(byDistrict[c.DistrictID], c)
		if c.IsBloodCenter && center == nil {
			center = &ref.Clinics[i]
		}
	}
	b := &csvBuilder{}
	b.add(shareCSVHeader)
	start, end := yearPeriod(cfg.Year)
	var firstDistrict string
	for _, d := range ref.Districts {
		clinics := byDistrict[d.DistrictID]
		if len(clinics) == 0 && center != nil {
			clinics = []refdata.Clinic{*center}
		}
		if len(clinics) == 0 {
			continue
		}
		sort.Slice(clinics, func(a, b int) bool { return clinics[a].ClinicID < clinics[b].ClinicID })
		weights := make([]int64, len(clinics))
		var total int64
		for i := range weights {
			weights[i] = int64(1 + rng.IntN(10))
			total += weights[i]
		}
		units := splitUnits(10000, weights, total)
		for i, c := range clinics {
			b.add(strings.Join([]string{d.DistrictCode, c.ClinicCode, shareText(units[i]), demoBasis, demoSource, start, end}, ";"))
		}
		if firstDistrict == "" {
			firstDistrict = d.DistrictCode
		}
	}
	if cfg.Broken {
		// Битые строки только в несуществующих районах: доли района принимаются целиком или никак,
		// и битая строка в настоящем районе отклонила бы и его корректные строки.
		b.addBad(strings.Join([]string{"D99", ref.Clinics[0].ClinicCode, "1", demoBasis, demoSource, start, end}, ";"))
		b.addBad(strings.Join([]string{"D98", "C999", "0", "", demoSource, start, end}, ";"))
		b.addBad(strings.Join([]string{"D97", ref.Clinics[0].ClinicCode, "1", demoBasis, demoSource, "", ""}, ";"))
	}
	return b.file(fmt.Sprintf("clinic_share_%d.csv", cfg.Year))
}

// splitUnits делит целое по весам методом наибольших остатков (сумма частей точно равна total).
func splitUnits(units int64, weights []int64, total int64) []int64 {
	out := make([]int64, len(weights))
	rem := make([]int64, len(weights))
	var given int64
	for i, w := range weights {
		out[i] = units * w / total
		rem[i] = units * w % total
		given += out[i]
	}
	order := make([]int, len(weights))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return rem[order[a]] > rem[order[b]] })
	for i := int64(0); i < units-given; i++ {
		out[order[i]]++
	}
	return out
}
