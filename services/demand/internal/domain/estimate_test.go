package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type lineKey struct {
	id    int64
	kind  string
	group string
}

func profileOf(id, population int64, shares map[string]Share) DistrictProfile {
	all := make(map[string]Share, len(testGroups))
	for _, g := range testGroups {
		all[g] = shares[g]
	}
	return DistrictProfile{DistrictID: id, DistrictCode: "", Population: population, GroupShares: all}
}

func history(t testing.TB, kind string, start, end [3]int, ml int64) ConsumptionRecord {
	return ConsumptionRecord{
		KindCode: kind, ConsumedVolumeML: ml, Source: HistorySourceFile,
		Period: period(t, date(start[0], time.Month(start[1]), start[2]), date(end[0], time.Month(end[1]), end[2])),
	}
}

func clinicLines(e Estimate) map[lineKey]int64 {
	out := map[lineKey]int64{}
	for _, l := range e.Clinics {
		out[lineKey{l.ClinicID, l.KindCode, l.BloodGroup}] += l.VolumeML
	}
	return out
}

func districtLines(e Estimate) map[lineKey]int64 {
	out := map[lineKey]int64{}
	for _, l := range e.Districts {
		out[lineKey{l.DistrictID, l.KindCode, l.BloodGroup}] += l.VolumeML
	}
	return out
}

// referenceInput — эталонный набор. Ручной расчёт:
//
//	Период оценки: январь 2026, 31 день.
//	B(RBC) = (1 810 000 + 1 840 000) мл × 31 / (181 + 184) дн = 3 650 000 × 31 / 365 = 310 000 мл.
//	B(CRYO) = 730 000 × 31 / 365 = 62 000 мл.
//	Население: D01 600 000, D02 400 000 → V(D01) = 0,6·B, V(D02) = 0,4·B.
//	  V(D01, RBC) = 186 000, V(D02, RBC) = 124 000; V(D01, CRYO) = 37 200, V(D02, CRYO) = 24 800.
//	Группы: D01 O+ 0,5, A+ 0,5; D02 O+, O-, A+, AB- по 0,25.
//	  D(D01, RBC, O+) = D(D01, RBC, A+) = 93 000; D(D02, RBC, g) = 31 000 для четырёх групп.
//	  CRYO без признака группы: D = V.
//	Доли клиник: D01 → C001 0,7, C002 0,3; D02 → C002 0,5, C003 0,5.
//	  C001: RBC O+ 65 100, A+ 65 100; CRYO 26 040.
//	  C002: RBC O+ 27 900 + 15 500 = 43 400, A+ 43 400, O- 15 500, AB- 15 500; CRYO 11 160 + 12 400 = 23 560.
//	  C003: RBC O+, O-, A+, AB- по 15 500; CRYO 12 400.
func referenceInput(t testing.TB) EstimateInput {
	return EstimateInput{
		Period: period(t, date(2026, 1, 1), date(2026, 1, 31)),
		Profiles: []DistrictProfile{
			profileOf(2, 400_000, map[string]Share{"O+": 0.25, "O-": 0.25, "A+": 0.25, "AB-": 0.25}),
			profileOf(1, 600_000, map[string]Share{"O+": 0.5, "A+": 0.5}),
		},
		History: []ConsumptionRecord{
			history(t, "RBC", [3]int{2025, 1, 1}, [3]int{2025, 6, 30}, 1_810_000),
			history(t, "RBC", [3]int{2025, 7, 1}, [3]int{2025, 12, 31}, 1_840_000),
			history(t, "CRYO", [3]int{2025, 1, 1}, [3]int{2025, 12, 31}, 730_000),
		},
		Shares: []ClinicShare{
			{DistrictID: 1, ClinicID: 1, Share: 0.7},
			{DistrictID: 1, ClinicID: 2, Share: 0.3},
			{DistrictID: 2, ClinicID: 2, Share: 0.5},
			{DistrictID: 2, ClinicID: 3, Share: 0.5},
		},
	}
}

func TestCalculateEstimate_ReferenceSet(t *testing.T) {
	e, err := CalculateEstimate(referenceInput(t), testRef(t))
	if err != nil {
		t.Fatal(err)
	}
	if e.Notice != DemoNotice {
		t.Fatalf("нет демо-пометки: %q", e.Notice)
	}
	wantBase := []KindBase{
		{KindCode: "RBC", VolumeML: 310_000, HistoryVolumeML: 3_650_000, HistoryDays: 365},
		{KindCode: "CRYO", VolumeML: 62_000, HistoryVolumeML: 730_000, HistoryDays: 365},
	}
	if len(e.Base) != len(wantBase) {
		t.Fatalf("база: %+v", e.Base)
	}
	for i := range wantBase {
		if e.Base[i] != wantBase[i] {
			t.Fatalf("база %d: %+v, ожидали %+v", i, e.Base[i], wantBase[i])
		}
	}

	wantDistricts := map[lineKey]int64{
		{1, "RBC", "O+"}: 93_000, {1, "RBC", "A+"}: 93_000,
		{2, "RBC", "O+"}: 31_000, {2, "RBC", "O-"}: 31_000, {2, "RBC", "A+"}: 31_000, {2, "RBC", "AB-"}: 31_000,
		{1, "CRYO", ""}: 37_200, {2, "CRYO", ""}: 24_800,
	}
	assertLines(t, "районы", districtLines(e), wantDistricts)

	wantClinics := map[lineKey]int64{
		{1, "RBC", "O+"}: 65_100, {1, "RBC", "A+"}: 65_100, {1, "CRYO", ""}: 26_040,
		{2, "RBC", "O+"}: 43_400, {2, "RBC", "A+"}: 43_400, {2, "RBC", "O-"}: 15_500, {2, "RBC", "AB-"}: 15_500,
		{2, "CRYO", ""}:  23_560,
		{3, "RBC", "O+"}: 15_500, {3, "RBC", "O-"}: 15_500, {3, "RBC", "A+"}: 15_500, {3, "RBC", "AB-"}: 15_500,
		{3, "CRYO", ""}: 12_400,
	}
	assertLines(t, "клиники", clinicLines(e), wantClinics)

	// Порядок строк клиник детерминирован: клиника, вид по справочнику, группа по справочнику.
	first := e.Clinics[0]
	if first.ClinicID != 1 || first.KindCode != "RBC" || first.BloodGroup != "O+" {
		t.Fatalf("первая строка клиник: %+v", first)
	}
	last := e.Clinics[len(e.Clinics)-1]
	if last.ClinicID != 3 || last.KindCode != "CRYO" {
		t.Fatalf("последняя строка клиник: %+v", last)
	}

	// Части «район → клиника» объясняют каждую строку клиники (NFR009).
	var c2O int64
	for _, p := range e.Parts {
		if p.ClinicID == 2 && p.KindCode == "RBC" && p.BloodGroup == "O+" {
			c2O += p.VolumeML
		}
	}
	if c2O != 43_400 {
		t.Fatalf("части C002 RBC O+: %d", c2O)
	}
}

// FR019: для вида без признака применимости разрез AB0/Rh не применяется.
func TestCalculateEstimate_NonGroupKindHasNoBloodGroup(t *testing.T) {
	e, err := CalculateEstimate(referenceInput(t), testRef(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range e.Clinics {
		if l.KindCode == "CRYO" && l.BloodGroup != "" {
			t.Fatalf("CRYO с группой: %+v", l)
		}
		if l.KindCode == "RBC" && l.BloodGroup == "" {
			t.Fatalf("RBC без группы: %+v", l)
		}
	}
}

func TestCalculateEstimate_ZeroLinesOmitted(t *testing.T) {
	e, err := CalculateEstimate(referenceInput(t), testRef(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range e.Clinics {
		if l.VolumeML == 0 {
			t.Fatalf("нулевая строка: %+v", l)
		}
	}
	for _, l := range e.Districts {
		if l.VolumeML == 0 {
			t.Fatalf("нулевая строка района: %+v", l)
		}
	}
}

func TestCalculateEstimate_RoundingEdges(t *testing.T) {
	one := map[string]Share{"O+": 1}
	cases := []struct {
		name      string
		histDays  [2][3]int
		histML    int64
		profiles  []DistrictProfile
		shares    []ClinicShare
		wantBase  int64
		wantClins map[lineKey]int64
	}{
		{
			name:     "база 1,5 мл округляется вверх до 2",
			histDays: [2][3]int{{2025, 1, 1}, {2025, 1, 2}}, histML: 3,
			profiles: []DistrictProfile{profileOf(1, 10, one)},
			shares:   []ClinicShare{{DistrictID: 1, ClinicID: 1, Share: 1}},
			wantBase: 2, wantClins: map[lineKey]int64{{1, "RBC", "O+"}: 2},
		},
		{
			name:     "база 0,5 мл округляется вверх до 1",
			histDays: [2][3]int{{2025, 1, 1}, {2025, 1, 2}}, histML: 1,
			profiles: []DistrictProfile{profileOf(1, 10, one)},
			shares:   []ClinicShare{{DistrictID: 1, ClinicID: 1, Share: 1}},
			wantBase: 1, wantClins: map[lineKey]int64{{1, "RBC", "O+"}: 1},
		},
		{
			name:     "база 0,25 мл округляется до 0",
			histDays: [2][3]int{{2025, 1, 1}, {2025, 1, 4}}, histML: 1,
			profiles: []DistrictProfile{profileOf(1, 10, one)},
			shares:   []ClinicShare{{DistrictID: 1, ClinicID: 1, Share: 1}},
			wantBase: 0, wantClins: map[lineKey]int64{},
		},
		{
			name:     "1 мл на два равных района — первому по идентификатору",
			histDays: [2][3]int{{2025, 1, 1}, {2025, 1, 1}}, histML: 1,
			profiles: []DistrictProfile{profileOf(2, 10, one), profileOf(1, 10, one)},
			shares:   []ClinicShare{{DistrictID: 1, ClinicID: 4, Share: 1}, {DistrictID: 2, ClinicID: 5, Share: 1}},
			wantBase: 1, wantClins: map[lineKey]int64{{4, "RBC", "O+"}: 1},
		},
		{
			name:     "100 мл на три равных района — 34, 33, 33",
			histDays: [2][3]int{{2025, 1, 1}, {2025, 1, 1}}, histML: 100,
			profiles: []DistrictProfile{profileOf(1, 7, one), profileOf(2, 7, one), profileOf(3, 7, one)},
			shares: []ClinicShare{
				{DistrictID: 1, ClinicID: 1, Share: 1}, {DistrictID: 2, ClinicID: 2, Share: 1}, {DistrictID: 3, ClinicID: 3, Share: 1},
			},
			wantBase:  100,
			wantClins: map[lineKey]int64{{1, "RBC", "O+"}: 34, {2, "RBC", "O+"}: 33, {3, "RBC", "O+"}: 33},
		},
		{
			name:     "10 мл на клиники по трети — наибольший остаток у доли 0,3334",
			histDays: [2][3]int{{2025, 1, 1}, {2025, 1, 1}}, histML: 10,
			profiles: []DistrictProfile{profileOf(1, 1, one)},
			shares: []ClinicShare{
				{DistrictID: 1, ClinicID: 1, Share: 0.3333}, {DistrictID: 1, ClinicID: 2, Share: 0.3333}, {DistrictID: 1, ClinicID: 3, Share: 0.3334},
			},
			wantBase:  10,
			wantClins: map[lineKey]int64{{1, "RBC", "O+"}: 3, {2, "RBC", "O+"}: 3, {3, "RBC", "O+"}: 4},
		},
		{
			name:     "доли с суммой 1,0001 нормируются и не добавляют мл",
			histDays: [2][3]int{{2025, 1, 1}, {2025, 1, 1}}, histML: 10_001,
			profiles: []DistrictProfile{profileOf(1, 1, one)},
			shares:   []ClinicShare{{DistrictID: 1, ClinicID: 1, Share: 0.5}, {DistrictID: 1, ClinicID: 2, Share: 0.5001}},
			wantBase: 10_001, wantClins: map[lineKey]int64{{1, "RBC", "O+"}: 5000, {2, "RBC", "O+"}: 5001},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := EstimateInput{
				Period:   period(t, date(2026, 1, 1), date(2026, 1, 1)),
				Profiles: c.profiles,
				History:  []ConsumptionRecord{history(t, "RBC", c.histDays[0], c.histDays[1], c.histML)},
				Shares:   c.shares,
			}
			e, err := CalculateEstimate(in, testRef(t))
			if err != nil {
				t.Fatal(err)
			}
			if len(e.Base) != 1 || e.Base[0].VolumeML != c.wantBase {
				t.Fatalf("база %+v, ожидали %d", e.Base, c.wantBase)
			}
			assertLines(t, "клиники", clinicLines(e), c.wantClins)
		})
	}
}

func TestCalculateEstimate_InvalidInput(t *testing.T) {
	ref := testRef(t)
	cases := []struct {
		name   string
		mutate func(*EstimateInput)
	}{
		{"нет профилей", func(in *EstimateInput) { in.Profiles = nil }},
		{"нет долей клиник", func(in *EstimateInput) { in.Shares = nil }},
		{"нет истории", func(in *EstimateInput) { in.History = nil }},
		{"период не задан", func(in *EstimateInput) { in.Period = Period{} }},
		{"район профиля без долей клиник", func(in *EstimateInput) { in.Shares = in.Shares[:2] }},
		{"сумма долей клиник 1,2", func(in *EstimateInput) { in.Shares[1].Share = 0.5 }},
		{"сумма долей групп 1,2", func(in *EstimateInput) { in.Profiles[1].GroupShares["O+"] = 0.7 }},
		{"нет доли группы", func(in *EstimateInput) { delete(in.Profiles[0].GroupShares, "AB-") }},
		{"район повторяется", func(in *EstimateInput) { in.Profiles = append(in.Profiles, in.Profiles[0]) }},
		{"пара район — клиника повторяется", func(in *EstimateInput) { in.Shares = append(in.Shares, in.Shares[0]) }},
		{"население 0", func(in *EstimateInput) { in.Profiles[0].Population = 0 }},
		{"неизвестный вид в истории", func(in *EstimateInput) { in.History[2].KindCode = "XYZ" }},
		{"пересечение периодов истории", func(in *EstimateInput) {
			in.History = append(in.History, history(t, "RBC", [3]int{2025, 6, 1}, [3]int{2025, 6, 30}, 1))
		}},
		{"отрицательный расход", func(in *EstimateInput) { in.History[0].ConsumedVolumeML = -1 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := referenceInput(t)
			c.mutate(&in)
			_, err := CalculateEstimate(in, ref)
			if !errors.Is(err, ErrEstimateInput) {
				t.Fatalf("ожидали ErrEstimateInput, получили %v", err)
			}
		})
	}
	if _, err := CalculateEstimate(referenceInput(t), nil); !errors.Is(err, ErrEstimateInput) {
		t.Fatalf("без справочника: %v", err)
	}
}

// Район есть в долях, но нет в профиле: версии входов несогласованы, B не делится между оставшимися.
func TestCalculateEstimate_SharesOfDistrictWithoutProfileRejected(t *testing.T) {
	in := referenceInput(t)
	in.Shares = append(in.Shares, ClinicShare{DistrictID: 5, ClinicID: 8, Share: 1})
	_, err := CalculateEstimate(in, testRef(t))
	if !errors.Is(err, ErrEstimateInput) {
		t.Fatalf("ожидали ErrEstimateInput, получили %v", err)
	}
	if !strings.Contains(err.Error(), "район D05 есть в долях, но нет в профиле") {
		t.Fatalf("сообщение: %v", err)
	}
}

func TestCalculateEstimate_PeriodYearsLimited(t *testing.T) {
	in := referenceInput(t)
	in.Period = Period{Start: date(1800, 1, 1), End: date(1800, 1, 31)}
	if _, err := CalculateEstimate(in, testRef(t)); !errors.Is(err, ErrEstimateInput) {
		t.Fatalf("период 1800 года: %v", err)
	}
}

func assertLines(t *testing.T, what string, got, want map[lineKey]int64) {
	t.Helper()
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s %+v: %d, ожидали %d", what, k, got[k], v)
		}
	}
	for k, v := range got {
		if _, ok := want[k]; !ok && v != 0 {
			t.Errorf("%s: лишняя строка %+v = %d", what, k, v)
		}
	}
}
