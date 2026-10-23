package domain

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// ErrEstimateInput — входные данные расчёта неполны или противоречивы; приложение отвечает 422.
var ErrEstimateInput = errors.New("входные данные оценки некорректны")

func invalidInput(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrEstimateInput, fmt.Sprintf(format, args...))
}

// EstimateInput — версии исходных данных, по которым считается и воспроизводится оценка (FR019, FR020):
// профиль районов из одного файла, выбранные периоды истории расхода, одна версия долей район → клиника.
type EstimateInput struct {
	Period   Period
	Profiles []DistrictProfile
	History  []ConsumptionRecord
	Shares   []ClinicShare
}

// KindBase — база B(k, p) и из чего она получена (NFR009: объяснимость расчёта).
type KindBase struct {
	KindCode        string
	VolumeML        int64
	HistoryVolumeML int64
	HistoryDays     int64
}

// DistrictDemand — D(r, k, g, p); BloodGroup пуст для видов без признака применимости группы.
type DistrictDemand struct {
	DistrictID int64
	KindCode   string
	BloodGroup string
	VolumeML   int64
}

// ClinicPart — часть D(r, k, g, p), отнесённая к клинике по доле w(r, c).
type ClinicPart struct {
	DistrictID int64
	ClinicID   int64
	KindCode   string
	BloodGroup string
	VolumeML   int64
}

// ClinicDemand — D_c = Σ_r w(r, c) · D: строка clinic_demand.
type ClinicDemand struct {
	ClinicID   int64
	KindCode   string
	BloodGroup string
	VolumeML   int64
}

// Estimate — результат расчёта. Строки с нулевым объёмом не включаются: отсутствие строки и есть ноль.
type Estimate struct {
	Period    Period
	Notice    string
	Base      []KindBase
	Districts []DistrictDemand
	Parts     []ClinicPart
	Clinics   []ClinicDemand
}

// CalculateEstimate считает фоновую потребность (FR019, глоссарий «Формулы»):
//
//  1. B(k, p) = Σ расхода вида × дней периода оценки / Σ дней периодов истории — средний суточный расход,
//     перенесённый на период оценки; округление до целого мл, половина — вверх.
//  2. V(r, k, p) — B, разделённая между районами пропорционально населению профиля.
//  3. D(r, k, g, p) = V × s(r, g) для видов с is_group_applicable, иначе D = V без группы.
//  4. D_c = Σ_r w(r, c) · D.
//
// Объёмы — целые мл; каждое деление — методом наибольших остатков (apportion), поэтому сумма по
// клиникам точно равна сумме по районам и базе B. Доли района в версии долей, для которого нет профиля,
// не дают потребности: делить нечего.
func CalculateEstimate(in EstimateInput, ref *Reference) (Estimate, error) {
	if ref == nil {
		return Estimate{}, invalidInput("справочники не загружены")
	}
	if in.Period.IsZero() || in.Period.End.Before(in.Period.Start) {
		return Estimate{}, invalidInput("период оценки не задан")
	}
	if len(in.Profiles) == 0 {
		return Estimate{}, invalidInput("нет профиля районов")
	}
	if len(in.History) == 0 {
		return Estimate{}, invalidInput("нет истории расхода")
	}
	if len(in.Shares) == 0 {
		return Estimate{}, invalidInput("нет долей распределения по клиникам")
	}
	profiles, err := checkProfiles(in.Profiles, ref)
	if err != nil {
		return Estimate{}, err
	}
	shares, err := checkShares(in.Shares, ref)
	if err != nil {
		return Estimate{}, err
	}
	for _, p := range profiles {
		if len(shares[p.DistrictID]) == 0 {
			return Estimate{}, invalidInput("для района %s нет долей распределения по клиникам", ref.districtCode(p.DistrictID))
		}
	}
	base, err := baseVolumes(in.History, in.Period, ref)
	if err != nil {
		return Estimate{}, err
	}

	population := make([]int64, len(profiles))
	for i, p := range profiles {
		population[i] = p.Population
	}
	est := Estimate{Period: in.Period, Notice: DemoNotice, Base: base}
	type clinicKey struct {
		clinic int64
		kind   string
		group  string
	}
	totals := map[clinicKey]int64{}
	for _, b := range base {
		kind, _ := ref.Kind(b.KindCode)
		v, err := apportion(b.VolumeML, population)
		if err != nil {
			return Estimate{}, invalidInput("вид %s: %v", b.KindCode, err)
		}
		for i, p := range profiles {
			groups, volumes, err := splitByGroups(v[i], p, kind, ref)
			if err != nil {
				return Estimate{}, err
			}
			districtShares := shares[p.DistrictID]
			weights := make([]int64, len(districtShares))
			for j, s := range districtShares {
				weights[j] = s.Share.TenThousandths()
			}
			for gi, d := range volumes {
				if d == 0 {
					continue
				}
				est.Districts = append(est.Districts, DistrictDemand{DistrictID: p.DistrictID, KindCode: b.KindCode,
					BloodGroup: groups[gi], VolumeML: d})
				parts, err := apportion(d, weights)
				if err != nil {
					return Estimate{}, invalidInput("район %s: %v", ref.districtCode(p.DistrictID), err)
				}
				for j, part := range parts {
					if part == 0 {
						continue
					}
					c := districtShares[j].ClinicID
					est.Parts = append(est.Parts, ClinicPart{DistrictID: p.DistrictID, ClinicID: c,
						KindCode: b.KindCode, BloodGroup: groups[gi], VolumeML: part})
					totals[clinicKey{c, b.KindCode, groups[gi]}] += part
				}
			}
		}
	}
	for k, v := range totals {
		est.Clinics = append(est.Clinics, ClinicDemand{ClinicID: k.clinic, KindCode: k.kind, BloodGroup: k.group, VolumeML: v})
	}
	sortLines(&est, ref)
	return est, nil
}

// checkProfiles проверяет профили и упорядочивает их по району: порядок решает, кому достаётся
// лишний мл при равных остатках, и должен быть воспроизводим.
func checkProfiles(in []DistrictProfile, ref *Reference) ([]DistrictProfile, error) {
	out := append([]DistrictProfile(nil), in...)
	sort.Slice(out, func(a, b int) bool { return out[a].DistrictID < out[b].DistrictID })
	for i, p := range out {
		code := ref.districtCode(p.DistrictID)
		if i > 0 && out[i-1].DistrictID == p.DistrictID {
			return nil, invalidInput("район %s указан в профиле дважды", code)
		}
		if p.Population <= 0 {
			return nil, invalidInput("район %s: население должно быть больше 0", code)
		}
		var sum int64
		for _, g := range ref.groups {
			s, ok := p.GroupShares[g]
			if !ok {
				return nil, invalidInput("район %s: нет доли группы %s", code, g)
			}
			u := s.TenThousandths()
			if u < 0 || u > shareScale {
				return nil, invalidInput("район %s: доля группы %s вне диапазона от 0 до 1", code, g)
			}
			sum += u
		}
		if !ShareSumWithinTolerance(sum) {
			return nil, invalidInput("район %s: сумма долей групп %s вне допуска 1 ± 0,0001", code, formatShareUnits(sum))
		}
	}
	return out, nil
}

// checkShares группирует доли по району, упорядочивает клиники по идентификатору и сверяет суммы (FR046).
func checkShares(in []ClinicShare, ref *Reference) (map[int64][]ClinicShare, error) {
	out := map[int64][]ClinicShare{}
	for _, s := range in {
		u := s.Share.TenThousandths()
		if u <= 0 || u > shareScale {
			return nil, invalidInput("район %s, клиника %d: доля должна быть больше 0 и не больше 1",
				ref.districtCode(s.DistrictID), s.ClinicID)
		}
		out[s.DistrictID] = append(out[s.DistrictID], s)
	}
	for d, list := range out {
		sort.Slice(list, func(a, b int) bool { return list[a].ClinicID < list[b].ClinicID })
		var sum int64
		for i, s := range list {
			if i > 0 && list[i-1].ClinicID == s.ClinicID {
				return nil, invalidInput("район %s: доля клиники %d указана дважды", ref.districtCode(d), s.ClinicID)
			}
			sum += s.Share.TenThousandths()
		}
		if !ShareSumWithinTolerance(sum) {
			return nil, invalidInput("сумма долей района %s по клиникам %s вне допуска 1 ± 0,0001",
				ref.districtCode(d), formatShareUnits(sum))
		}
	}
	return out, nil
}

// baseVolumes считает B(k, p) по видам в порядке справочника. Пересекающиеся периоды одного вида
// запрещены: иначе расход посчитался бы дважды (например, файл и выгрузка C01 за один месяц).
func baseVolumes(history []ConsumptionRecord, p Period, ref *Reference) ([]KindBase, error) {
	byKind := map[string][]ConsumptionRecord{}
	for _, h := range history {
		if _, ok := ref.Kind(h.KindCode); !ok {
			return nil, invalidInput("неизвестный вид компонента %q в истории расхода", h.KindCode)
		}
		if h.ConsumedVolumeML < 0 {
			return nil, invalidInput("вид %s: отрицательный расход", h.KindCode)
		}
		if h.Period.IsZero() || h.Period.End.Before(h.Period.Start) {
			return nil, invalidInput("вид %s: период истории не задан", h.KindCode)
		}
		for _, prev := range byKind[h.KindCode] {
			if prev.Period.Overlaps(h.Period) {
				return nil, invalidInput("вид %s: периоды истории %s–%s и %s–%s пересекаются", h.KindCode,
					prev.Period.Start.Format(dateLayout), prev.Period.End.Format(dateLayout),
					h.Period.Start.Format(dateLayout), h.Period.End.Format(dateLayout))
			}
		}
		byKind[h.KindCode] = append(byKind[h.KindCode], h)
	}
	var out []KindBase
	for _, k := range ref.kinds {
		records := byKind[k.Code]
		if len(records) == 0 {
			continue
		}
		var volume, days int64
		for _, r := range records {
			if volume > math.MaxInt64-r.ConsumedVolumeML {
				return nil, invalidInput("вид %s: расход слишком велик", k.Code)
			}
			volume += r.ConsumedVolumeML
			days += r.Period.Days()
		}
		b, err := mulDivRoundHalfUp(volume, p.Days(), days)
		if err != nil {
			return nil, invalidInput("вид %s: %v", k.Code, err)
		}
		out = append(out, KindBase{KindCode: k.Code, VolumeML: b, HistoryVolumeML: volume, HistoryDays: days})
	}
	return out, nil
}

// splitByGroups делит V района по долям групп s(r, g); для вида без признака группы — одна часть без группы.
func splitByGroups(v int64, p DistrictProfile, kind KindRef, ref *Reference) ([]string, []int64, error) {
	if !kind.IsGroupApplicable {
		return []string{""}, []int64{v}, nil
	}
	weights := make([]int64, len(ref.groups))
	for i, g := range ref.groups {
		weights[i] = p.GroupShares[g].TenThousandths()
	}
	volumes, err := apportion(v, weights)
	if err != nil {
		return nil, nil, invalidInput("район %s: %v", ref.districtCode(p.DistrictID), err)
	}
	return ref.groups, volumes, nil
}

// sortLines задаёт воспроизводимый порядок строк: район или клиника, вид и группа по справочнику.
func sortLines(e *Estimate, ref *Reference) {
	kind := func(code string) int { return ref.kindIndex[code] }
	group := func(code string) int {
		if i, ok := ref.groupIndex[code]; ok {
			return i
		}
		return -1
	}
	less := func(id1, id2 int64, k1, k2, g1, g2 string) bool {
		if id1 != id2 {
			return id1 < id2
		}
		if kind(k1) != kind(k2) {
			return kind(k1) < kind(k2)
		}
		return group(g1) < group(g2)
	}
	sort.Slice(e.Districts, func(a, b int) bool {
		x, y := e.Districts[a], e.Districts[b]
		return less(x.DistrictID, y.DistrictID, x.KindCode, y.KindCode, x.BloodGroup, y.BloodGroup)
	})
	sort.SliceStable(e.Parts, func(a, b int) bool {
		x, y := e.Parts[a], e.Parts[b]
		if x.DistrictID != y.DistrictID {
			return x.DistrictID < y.DistrictID
		}
		return less(x.ClinicID, y.ClinicID, x.KindCode, y.KindCode, x.BloodGroup, y.BloodGroup)
	})
	sort.Slice(e.Clinics, func(a, b int) bool {
		x, y := e.Clinics[a], e.Clinics[b]
		return less(x.ClinicID, y.ClinicID, x.KindCode, y.KindCode, x.BloodGroup, y.BloodGroup)
	})
}
