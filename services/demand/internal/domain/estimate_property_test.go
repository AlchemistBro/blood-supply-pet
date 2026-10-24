package domain

import (
	"sort"
	"testing"

	"pgregory.net/rapid"
)

// cutUnits — n положительных (или неотрицательных при allowZero) целых частей с суммой 10 000:
// доли в десятитысячных ровно на 1, как после проверки файла.
func cutUnits(t *rapid.T, label string, n int, allowZero bool) []int64 {
	var inner []int
	if allowZero {
		inner = rapid.SliceOfN(rapid.IntRange(0, shareScale), n-1, n-1).Draw(t, label)
	} else {
		// Различные точки разреза внутри (0; 10 000) дают строго положительные доли клиник.
		inner = rapid.SliceOfNDistinct(rapid.IntRange(1, shareScale-1), n-1, n-1, rapid.ID[int]).Draw(t, label)
	}
	cuts := append([]int{0, shareScale}, inner...)
	sort.Ints(cuts)
	out := make([]int64, n)
	for i := 0; i < n; i++ {
		out[i] = int64(cuts[i+1] - cuts[i])
	}
	return out
}

// Свойство FR019: при любых профилях, долях и истории сумма по клиникам равна потребности района —
// для каждого района, вида и группы, — а сумма по районам равна базе B вида. Округление не теряет мл.
func TestCalculateEstimate_PropertyClinicSumEqualsDistrictDemand(t *testing.T) {
	ref := testRef(t)
	rapid.Check(t, func(t *rapid.T) {
		nDistricts := rapid.IntRange(1, 6).Draw(t, "districts")
		var in EstimateInput
		start := date(2026, 1, 1)
		days := rapid.IntRange(1, 366).Draw(t, "period_days")
		in.Period = Period{Start: start, End: start.AddDate(0, 0, days-1)}

		for d := 1; d <= nDistricts; d++ {
			groupUnits := cutUnits(t, "group_cut", len(testGroups), true)
			shares := make(map[string]Share, len(testGroups))
			for i, g := range testGroups {
				shares[g] = Share(float64(groupUnits[i]) / shareScale)
			}
			in.Profiles = append(in.Profiles, profileOf(int64(d), rapid.Int64Range(1, 5_000_000).Draw(t, "population"), shares))

			nClinics := rapid.IntRange(1, 4).Draw(t, "clinics")
			clinics := rapid.Permutation([]int64{1, 2, 3, 4, 5, 6, 7, 8}).Draw(t, "clinic_ids")[:nClinics]
			units := cutUnits(t, "clinic_cut", nClinics, false)
			for i, c := range clinics {
				in.Shares = append(in.Shares, ClinicShare{DistrictID: int64(d), ClinicID: c, Share: Share(float64(units[i]) / shareScale)})
			}
		}
		for _, kind := range []string{"RBC", "FFP", "PLT", "CRYO"} {
			if !rapid.Bool().Draw(t, "has_history") {
				continue
			}
			hStart := date(2024, 1, 1).AddDate(0, 0, rapid.IntRange(0, 300).Draw(t, "history_offset"))
			hDays := rapid.IntRange(1, 400).Draw(t, "history_days")
			in.History = append(in.History, ConsumptionRecord{
				KindCode: kind, Source: HistorySourceFile,
				Period:           Period{Start: hStart, End: hStart.AddDate(0, 0, hDays-1)},
				ConsumedVolumeML: rapid.Int64Range(0, 1_000_000_000).Draw(t, "consumed_ml"),
			})
		}
		if len(in.History) == 0 {
			in.History = append(in.History, ConsumptionRecord{KindCode: "RBC", Source: HistorySourceFile,
				Period: Period{Start: start, End: start}, ConsumedVolumeML: rapid.Int64Range(0, 1_000_000).Draw(t, "rbc_ml")})
		}

		e, err := CalculateEstimate(in, ref)
		if err != nil {
			t.Fatalf("расчёт: %v", err)
		}

		type dkg struct {
			district int64
			kind     string
			group    string
		}
		type ckg struct {
			clinic int64
			kind   string
			group  string
		}
		districtDemand := map[dkg]int64{}
		perKindDistricts := map[string]int64{}
		for _, l := range e.Districts {
			districtDemand[dkg{l.DistrictID, l.KindCode, l.BloodGroup}] += l.VolumeML
			perKindDistricts[l.KindCode] += l.VolumeML
		}
		partsByDistrict := map[dkg]int64{}
		partsByClinic := map[ckg]int64{}
		for _, p := range e.Parts {
			partsByDistrict[dkg{p.DistrictID, p.KindCode, p.BloodGroup}] += p.VolumeML
			partsByClinic[ckg{p.ClinicID, p.KindCode, p.BloodGroup}] += p.VolumeML
		}
		perKindClinics := map[string]int64{}
		clinicLines := map[ckg]int64{}
		for _, l := range e.Clinics {
			perKindClinics[l.KindCode] += l.VolumeML
			clinicLines[ckg{l.ClinicID, l.KindCode, l.BloodGroup}] += l.VolumeML
		}

		// Σ_c части клиник района = D(r, k, g).
		for k, v := range districtDemand {
			if partsByDistrict[k] != v {
				t.Fatalf("район %+v: сумма по клиникам %d ≠ потребность района %d", k, partsByDistrict[k], v)
			}
		}
		for k, v := range partsByDistrict {
			if districtDemand[k] != v {
				t.Fatalf("части %+v без строки района: %d", k, v)
			}
		}
		// Строка клиники = Σ_r её частей.
		for k, v := range partsByClinic {
			if clinicLines[k] != v {
				t.Fatalf("клиника %+v: строка %d ≠ сумма частей %d", k, clinicLines[k], v)
			}
		}
		// Σ по клиникам = Σ по районам = B(k).
		for _, b := range e.Base {
			if perKindDistricts[b.KindCode] != b.VolumeML || perKindClinics[b.KindCode] != b.VolumeML {
				t.Fatalf("вид %s: база %d, районы %d, клиники %d", b.KindCode, b.VolumeML,
					perKindDistricts[b.KindCode], perKindClinics[b.KindCode])
			}
		}
		// Разрез по группам только у видов с признаком применимости (FR019).
		for _, l := range e.Clinics {
			kind, _ := ref.Kind(l.KindCode)
			if kind.IsGroupApplicable == (l.BloodGroup == "") {
				t.Fatalf("строка %+v: группа не соответствует признаку вида", l)
			}
			if l.VolumeML <= 0 {
				t.Fatalf("строка с неположительным объёмом: %+v", l)
			}
		}
	})
}

// Свойство: каждая часть деления отличается от точной пропорции меньше чем на 1 мл.
func TestApportion_PropertyWithinOneMlOfExactQuota(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		total := rapid.Int64Range(0, 1_000_000_000_000).Draw(t, "total")
		weights := rapid.SliceOfN(rapid.Int64Range(0, 10_000_000), 1, 12).Draw(t, "weights")
		var sum int64
		for _, w := range weights {
			sum += w
		}
		if sum == 0 {
			weights[0] = 1
			sum = 1
		}
		got, err := apportion(total, weights)
		if err != nil {
			t.Fatal(err)
		}
		var s int64
		for i, v := range got {
			s += v
			// Квота во float64 только для сравнения; запас 0,001 мл покрывает её двоичную погрешность при 10^12.
			quota := float64(total) * float64(weights[i]) / float64(sum)
			if diff := float64(v) - quota; diff <= -1.001 || diff >= 1.001 {
				t.Fatalf("часть %d = %d, точная квота %.3f", i, v, quota)
			}
			if weights[i] == 0 && v != 0 {
				t.Fatalf("нулевой вес получил %d", v)
			}
		}
		if s != total {
			t.Fatalf("сумма %d ≠ %d", s, total)
		}
	})
}
