package domain

import (
	"cmp"
	"fmt"
	"math"
	"slices"

	"github.com/google/uuid"
)

// splitByWeights делит целый объём total пропорционально весам методом наибольшего остатка.
// Веса нормируются на свою сумму, неположительные веса долю не получают. Сумма частей равна total
// (или 0, если положительных весов нет), поэтому распределение никогда не превышает исходную величину
// (FR024). Ничья по остатку отдаётся меньшему индексу — расчёт детерминирован.
func splitByWeights(total int64, weights []float64) []int64 {
	out := make([]int64, len(weights))
	var sum float64
	for _, w := range weights {
		if w > 0 {
			sum += w
		}
	}
	if total <= 0 || sum <= 0 {
		return out
	}
	type rest struct {
		i    int
		frac float64
	}
	rests := make([]rest, 0, len(weights))
	var assigned int64
	for i, w := range weights {
		if w <= 0 {
			continue
		}
		exact := float64(total) * w / sum
		floor := int64(math.Floor(exact))
		out[i] = floor
		assigned += floor
		rests = append(rests, rest{i: i, frac: exact - float64(floor)})
	}
	slices.SortStableFunc(rests, func(a, b rest) int {
		if a.frac != b.frac {
			return cmp.Compare(b.frac, a.frac)
		}
		return cmp.Compare(a.i, b.i)
	})
	for k := 0; assigned < total; k++ {
		out[rests[k%len(rests)].i]++
		assigned++
	}
	// Погрешность float64 может дать сумму округлений вниз больше целого: снимаем с наименьших остатков.
	for k := len(rests) - 1; assigned > total; k-- {
		if k < 0 {
			k = len(rests) - 1
		}
		if out[rests[k].i] > 0 {
			out[rests[k].i]--
			assigned--
		}
	}
	return out
}

// floorML переводит объём из numeric в целые мл вниз: распределённое не должно превысить прогноз.
func floorML(v float64) int64 { return int64(math.Floor(v)) }

// roundML округляет фоновую потребность до мл по правилу «половина от нуля».
func roundML(v float64) int64 { return int64(math.Round(v)) }

// ceilML округляет норматив вверх: обязательный запас не занижается.
func ceilML(v float64) int64 { return int64(math.Ceil(v)) }

// groupPart — часть объёма, отнесённая к группе.
type groupPart struct {
	group  string
	volume int64
}

// splitGroups относит целый объём к группам: вид без группы — одна часть с пустой группой; группа задана —
// целиком к ней; группа не задана — по долям групп населения (демо, ADR-0010). Возвращает признак того,
// что группа была не определена. Без долей групп объём вида с группой разложить некуда — это ошибка входа,
// а не молчаливая потеря потребности.
func splitGroups(volume int64, kind Kind, group string, groups []GroupShare) ([]groupPart, bool, error) {
	if !kind.GroupApplicable {
		return []groupPart{{group: "", volume: volume}}, false, nil
	}
	if group != "" {
		return []groupPart{{group: group, volume: volume}}, false, nil
	}
	if len(groups) == 0 {
		return nil, true, fmt.Errorf("%w: вид %s с группой, группа не указана, а долей групп нет", ErrInvalidInput, kind.Code)
	}
	weights := make([]float64, len(groups))
	for i, g := range groups {
		weights[i] = g.Share
	}
	parts := splitByWeights(volume, weights)
	out := make([]groupPart, 0, len(groups))
	for i, g := range groups {
		out = append(out, groupPart{group: g.Group, volume: parts[i]})
	}
	return out, true, nil
}

// badFloat — NaN, бесконечность, отрицательное или больше предела.
func badFloat(v, limit float64) bool {
	return math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > limit
}

// sortedGroups возвращает доли групп по коду и проверяет их: повтор, отрицательная доля, сумма не 1.
func sortedGroups(groups []GroupShare) ([]GroupShare, error) {
	out := slices.Clone(groups)
	slices.SortFunc(out, func(a, b GroupShare) int { return cmp.Compare(a.Group, b.Group) })
	var sum float64
	for i, g := range out {
		if g.Group == "" || badFloat(g.Share, maxShare) {
			return nil, fmt.Errorf("%w: доля группы %q = %v", ErrInvalidInput, g.Group, g.Share)
		}
		if i > 0 && out[i-1].Group == g.Group {
			return nil, fmt.Errorf("%w: группа %s повторяется", ErrInvalidInput, g.Group)
		}
		sum += g.Share
	}
	if len(out) > 0 && math.Abs(sum-1) > 1e-6 {
		return nil, fmt.Errorf("%w: сумма долей групп %.6f, ожидали 1", ErrInvalidInput, sum)
	}
	return out, nil
}

// checkGroup проверяет, что группа известна (для вида с группой) — иначе потребность «потеряется» мимо запаса.
func checkGroup(kind Kind, group string, groups []GroupShare) error {
	if !kind.GroupApplicable || group == "" {
		return nil
	}
	if _, ok := slices.BinarySearchFunc(groups, group, func(g GroupShare, code string) int { return cmp.Compare(g.Group, code) }); !ok {
		return fmt.Errorf("%w: неизвестная группа %q", ErrInvalidInput, group)
	}
	return nil
}

// clinicWeights считает долю каждой клиники в территории прогноза: Σ_r (вес района / сумма весов) · w(r, c).
// Районы и клиники обходятся по возрастанию идентификатора — сумма float64 не зависит от порядка входа.
// Район территории с положительным весом, у которого нет ни одной положительной доли «район → клиника», —
// ошибка входа: иначе его часть сценарной потребности молча ушла бы другим районам или пропала.
// Запись дефицита здесь не подходит: дефицит относится к клинике, а клиники у этой части нет.
func clinicWeights(territory []DistrictWeight, shares []ClinicShare) ([]int64, []float64, error) {
	terr := slices.Clone(territory)
	slices.SortFunc(terr, func(a, b DistrictWeight) int { return cmp.Compare(a.DistrictID, b.DistrictID) })
	var total float64
	for i, d := range terr {
		if badFloat(d.Weight, maxWeight) {
			return nil, nil, fmt.Errorf("%w: вес района %d = %v", ErrInvalidInput, d.DistrictID, d.Weight)
		}
		if i > 0 && terr[i-1].DistrictID == d.DistrictID {
			return nil, nil, fmt.Errorf("%w: район %d повторяется в территории прогноза", ErrInvalidInput, d.DistrictID)
		}
		total += d.Weight
	}
	if total <= 0 {
		return nil, nil, fmt.Errorf("%w: территория прогноза не задана, а принимающих клиник нет", ErrInvalidInput)
	}
	byDistrict := map[int64][]ClinicShare{}
	for _, s := range shares {
		byDistrict[s.DistrictID] = append(byDistrict[s.DistrictID], s)
	}
	weight := map[int64]float64{}
	for _, d := range terr {
		if d.Weight == 0 {
			continue
		}
		list := byDistrict[d.DistrictID]
		slices.SortFunc(list, func(a, b ClinicShare) int { return cmp.Compare(a.ClinicID, b.ClinicID) })
		positive := false
		for _, s := range list {
			if s.Share > 0 {
				positive = true
				weight[s.ClinicID] += d.Weight / total * s.Share
			}
		}
		if !positive {
			return nil, nil, fmt.Errorf("%w: у района %d территории прогноза нет долей «район → клиника» (FR046)", ErrInvalidInput, d.DistrictID)
		}
	}
	clinics := make([]int64, 0, len(weight))
	for c := range weight {
		clinics = append(clinics, c)
	}
	slices.Sort(clinics)
	ws := make([]float64, len(clinics))
	for i, c := range clinics {
		ws[i] = weight[c]
	}
	return clinics, ws, nil
}

// checkClinicShares проверяет доли «район → клиника»: без повторов и отрицательных значений.
func checkClinicShares(shares []ClinicShare) error {
	seen := map[[2]int64]bool{}
	for _, s := range shares {
		if badFloat(s.Share, maxShare) {
			return fmt.Errorf("%w: доля клиники %d в районе %d = %v", ErrInvalidInput, s.ClinicID, s.DistrictID, s.Share)
		}
		key := [2]int64{s.DistrictID, s.ClinicID}
		if seen[key] {
			return fmt.Errorf("%w: доля клиники %d в районе %d повторяется", ErrInvalidInput, s.ClinicID, s.DistrictID)
		}
		seen[key] = true
	}
	return nil
}

// distributeForecast распределяет сценарную потребность прогноза ЧС по клиникам и группам (FR024):
// строка с принимающей клиникой — к ней (источник долей — прогноз C02); строка без неё — по долям
// «район → клиника» в пределах территории прогноза с пометкой «грубая оценка». Объём строки округляется
// вниз до мл и делится методом наибольшего остатка, поэтому сумма распределённого не превышает прогноз.
func distributeForecast(f *Forecast, shares []ClinicShare, kinds map[string]Kind, groupShares []GroupShare) ([]EmergencyAllocation, error) {
	if f == nil {
		return nil, nil
	}
	groups, err := sortedGroups(groupShares)
	if err != nil {
		return nil, err
	}
	if err := checkClinicShares(shares); err != nil {
		return nil, err
	}
	lines := slices.Clone(f.Lines)
	slices.SortFunc(lines, func(a, b ForecastLine) int { return compareUUID(a.LineID, b.LineID) })
	var (
		out          []EmergencyAllocation
		roughClinics []int64
		roughWeights []float64
		roughReady   bool
	)
	for i, l := range lines {
		if i > 0 && lines[i-1].LineID == l.LineID {
			return nil, fmt.Errorf("%w: строка прогноза %s повторяется", ErrInvalidInput, l.LineID)
		}
		kind, ok := kinds[l.Kind]
		if !ok {
			return nil, fmt.Errorf("%w: строка прогноза %s: неизвестный вид %q", ErrInvalidInput, l.LineID, l.Kind)
		}
		if badFloat(l.VolumeML, maxVolumeML) {
			return nil, fmt.Errorf("%w: строка прогноза %s: объём %v", ErrInvalidInput, l.LineID, l.VolumeML)
		}
		if err := checkGroup(kind, l.Group, groups); err != nil {
			return nil, fmt.Errorf("строка прогноза %s: %w", l.LineID, err)
		}
		total := floorML(l.VolumeML)
		var (
			clinics []int64
			parts   []int64
			origin  = SourceForecastC02
		)
		if l.ClinicID != 0 {
			clinics, parts = []int64{l.ClinicID}, []int64{total}
		} else {
			if !roughReady {
				roughClinics, roughWeights, err = clinicWeights(f.Territory, shares)
				if err != nil {
					return nil, err
				}
				roughReady = true
			}
			clinics, parts, origin = roughClinics, splitByWeights(total, roughWeights), SourceDistrictShares
		}
		for ci, c := range clinics {
			gparts, undetermined, err := splitGroups(parts[ci], kind, l.Group, groups)
			if err != nil {
				return nil, fmt.Errorf("строка прогноза %s: %w", l.LineID, err)
			}
			for _, gp := range gparts {
				if gp.volume <= 0 {
					continue
				}
				out = append(out, EmergencyAllocation{
					LineID: l.LineID, ClinicID: c, Kind: kind.Code, Group: gp.group, VolumeML: gp.volume,
					Source: origin, IsRoughEstimate: origin == SourceDistrictShares, GroupUndetermined: undetermined,
				})
			}
		}
	}
	slices.SortStableFunc(out, func(a, b EmergencyAllocation) int {
		return cmp.Or(cmp.Compare(a.ClinicID, b.ClinicID), cmp.Compare(a.Kind, b.Kind),
			cmp.Compare(a.Group, b.Group), compareUUID(a.LineID, b.LineID))
	})
	return out, nil
}

// compareUUID сравнивает идентификаторы побайтно — для детерминированного порядка.
func compareUUID(a, b uuid.UUID) int {
	for i := range a {
		if c := cmp.Compare(a[i], b[i]); c != 0 {
			return c
		}
	}
	return 0
}
