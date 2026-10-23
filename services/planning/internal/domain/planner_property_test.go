package domain

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"pgregory.net/rapid"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
)

// Свойственные тесты плана (NFR013, NFR014, ПП РФ № 641, п. 62 а). Эталонные величины здесь считаются
// заново по формулам канона, а не функциями пакета: иначе ошибка в формуле прошла бы незамеченной
// (проверка «тест действительно проверяет» — убрать слагаемое обязательств из защищённого объёма).

// genInput строит небольшой, но разнообразный вход: несколько клиник, оба вида, единицы в разных
// статусах и сроках, нормативы, фон, прогноз с принимающими клиниками и по долям районов,
// подтверждённые и неподтверждённые обязательства, неполная матрица оценок доставки.
func genInput(t *rapid.T) Input {
	n := rapid.IntRange(2, 6).Draw(t, "clinics")
	clinic := rapid.Int64Range(1, int64(n))
	kind := rapid.SampledFrom([]string{"RBC", "RBC", "CRYO"})
	group := rapid.SampledFrom([]string{"O+", "A+"})
	groupOrNone := rapid.SampledFrom([]string{"", "O+", "A+"})

	in := baseInput()
	in.Params.MaxFallbackSources = rapid.IntRange(0, 3).Draw(t, "fallback")
	share := rapid.Float64Range(0, 1).Draw(t, "share O+")
	in.GroupShares = []GroupShare{{Group: "O+", Share: share}, {Group: "A+", Share: 1 - share}}

	for c := int64(1); c <= int64(n); c++ {
		if rapid.Bool().Draw(t, "norm RBC") {
			in.Norms = append(in.Norms, Norm{ClinicID: c, Kind: "RBC", NormML: rapid.Float64Range(0, 3000).Draw(t, "norm RBC ml")})
		}
		if rapid.Bool().Draw(t, "norm CRYO") {
			in.Norms = append(in.Norms, Norm{ClinicID: c, Kind: "CRYO", NormML: rapid.Float64Range(0, 200).Draw(t, "norm CRYO ml")})
		}
		district := c%2 + 1
		in.ClinicShares = append(in.ClinicShares, ClinicShare{DistrictID: district, ClinicID: c, Share: rapid.Float64Range(0, 1).Draw(t, "clinic share")})
	}
	for i, nb := 0, rapid.IntRange(0, 6).Draw(t, "background"); i < nb; i++ {
		in.Background = append(in.Background, Background{
			ClinicID: clinic.Draw(t, "bg clinic"), Kind: kind.Draw(t, "bg kind"), Group: groupOrNone.Draw(t, "bg group"),
			VolumeML: rapid.Float64Range(0, 1500).Draw(t, "bg ml"),
		})
	}
	if rapid.Bool().Draw(t, "forecast") {
		f := &Forecast{
			ForecastID: uuid.NewSHA1(uuid.NameSpaceURL, []byte("forecast")), ScenarioVersion: "prop",
			Territory: []DistrictWeight{
				{DistrictID: 1, Weight: rapid.Float64Range(0.1, 3).Draw(t, "w1")},
				{DistrictID: 2, Weight: rapid.Float64Range(0, 3).Draw(t, "w2")},
			},
		}
		for i, nl := 0, rapid.IntRange(1, 4).Draw(t, "lines"); i < nl; i++ {
			f.Lines = append(f.Lines, ForecastLine{
				LineID: unitN(9000 + i), ClinicID: rapid.Int64Range(0, int64(n)).Draw(t, "line clinic"),
				Kind: kind.Draw(t, "line kind"), Group: groupOrNone.Draw(t, "line group"),
				VolumeML: rapid.Float64Range(0, 6000).Draw(t, "line ml"),
			})
		}
		in.Forecast = f
	}
	for i, nu := 0, rapid.IntRange(0, 80).Draw(t, "units"); i < nu; i++ {
		k := kind.Draw(t, "unit kind")
		volume := rapid.Int64Range(250, 350).Draw(t, "rbc ml")
		if k == "CRYO" {
			volume = rapid.Int64Range(15, 25).Draw(t, "cryo ml")
		}
		in.Units = append(in.Units, Unit{
			UnitID: unitN(i), ClinicID: clinic.Draw(t, "unit clinic"), Kind: k, Group: group.Draw(t, "unit group"), VolumeML: volume,
			ExpiresAt: t0.Add(time.Duration(rapid.IntRange(-48, 24*40).Draw(t, "expires h")) * time.Hour),
			Status:    rapid.SampledFrom([]string{UnitStatusInStock, UnitStatusInStock, UnitStatusInStock, UnitStatusQuarantine, "ISSUED"}).Draw(t, "status"),
		})
	}
	for i, no := 0, rapid.IntRange(0, 6).Draw(t, "obligations"); i < no; i++ {
		src := clinic.Draw(t, "obl source")
		tgt := clinic.Draw(t, "obl target")
		if tgt == src {
			tgt = src%int64(n) + 1
		}
		in.Obligations = append(in.Obligations, Obligation{
			ObligationID: unitN(5000 + i), SourceClinicID: src, TargetClinicID: tgt,
			Kind: kind.Draw(t, "obl kind"), Group: group.Draw(t, "obl group"), VolumeML: rapid.Int64Range(0, 1500).Draw(t, "obl ml"),
			Status: rapid.SampledFrom([]string{ObligationConfirmed, ObligationConfirmed, ObligationTransportRequested,
				ObligationDispatched, ObligationPending, "CANCELLED"}).Draw(t, "obl status"),
		})
	}
	for s := int64(1); s <= int64(n); s++ {
		for d := int64(1); d <= int64(n); d++ {
			if s == d || !rapid.SampledFrom([]bool{true, true, true, false}).Draw(t, "route") {
				continue
			}
			in.Delivery = append(in.Delivery, DeliveryEstimate{
				SourceClinicID: s, TargetClinicID: d, Minutes: rapid.IntRange(5, 600).Draw(t, "minutes"),
				EstimatedAt: t0.Add(-time.Duration(rapid.IntRange(0, 600).Draw(t, "age")) * time.Minute),
			})
		}
	}
	return in
}

func buildProp(t *rapid.T, in Input) *Plan {
	plan, err := NewPlanner(clock.NewMutable(t0)).Build(in)
	if err != nil {
		t.Fatalf("расчёт плана: %v", err)
	}
	return plan
}

// refSource — эталон остатка источника по канону: доступные единицы (IN_STOCK, годные на момент
// расчёта) и защищённый объём = норматив + фон за период + подтверждённые обязательства (FR025).
type refSource struct {
	availML, availUnits int64
	protectedML         int64
	protectedUnits      int64
}

func referenceSources(in Input) map[kindKey]*refSource {
	typical := map[string]int64{}
	for _, k := range in.Kinds {
		typical[k.Code] = k.TypicalVolumeML
	}
	out := map[kindKey]*refSource{}
	get := func(c int64, k string) *refSource {
		key := kindKey{clinic: c, kind: k}
		if out[key] == nil {
			out[key] = &refSource{}
		}
		return out[key]
	}
	for _, u := range in.Units {
		if u.Status == "IN_STOCK" && u.ExpiresAt.After(t0) {
			r := get(u.ClinicID, u.Kind)
			r.availML += u.VolumeML
			r.availUnits++
		}
	}
	for _, n := range in.Norms {
		get(n.ClinicID, n.Kind).protectedML += int64(math.Ceil(n.NormML))
	}
	for _, b := range in.Background {
		get(b.ClinicID, b.Kind).protectedML += int64(math.Round(b.VolumeML))
	}
	for _, o := range in.Obligations {
		switch o.Status {
		case "CONFIRMED", "TRANSPORT_REQUESTED", "DISPATCHED":
			get(o.SourceClinicID, o.Kind).protectedML += o.VolumeML
		}
	}
	for k, r := range out {
		r.protectedUnits = int64(math.Ceil(float64(r.protectedML) / float64(typical[k.kind])))
	}
	return out
}

func TestPropertyProposalNeverBreachesProtectedVolume(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := genInput(t)
		plan := buildProp(t, in)
		ref := referenceSources(in)
		donatedML := map[kindKey]int64{}
		donatedUnits := map[kindKey]int64{}
		for _, p := range plan.Proposals {
			k := kindKey{clinic: p.SourceClinicID, kind: p.Kind}
			donatedML[k] += p.VolumeML
			donatedUnits[k] += int64(p.Units)
		}
		for k, d := range donatedML {
			r := ref[k]
			if r == nil {
				t.Fatalf("источник %d/%s без доступных единиц отдаёт %d мл", k.clinic, k.kind, d)
			}
			if r.availML-d < r.protectedML {
				t.Fatalf("источник %d/%s: после плана %d мл ниже защищённого объёма %d мл (NFR013)", k.clinic, k.kind, r.availML-d, r.protectedML)
			}
			if r.availUnits-donatedUnits[k] < r.protectedUnits {
				t.Fatalf("источник %d/%s: после плана %d ед. ниже защищённого объёма %d ед.", k.clinic, k.kind, r.availUnits-donatedUnits[k], r.protectedUnits)
			}
		}
	})
}

func TestPropertyOnlyAvailableUnitsAreProposed(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := genInput(t)
		plan := buildProp(t, in)
		units := map[uuid.UUID]Unit{}
		for _, u := range in.Units {
			units[u.UnitID] = u
		}
		groupKind := map[string]bool{"RBC": true}
		used := map[uuid.UUID]bool{}
		for _, p := range plan.Proposals {
			if p.SourceClinicID == p.TargetClinicID {
				t.Fatalf("предложение %s: источник совпадает с получателем", p.ProposalID)
			}
			var volume int64
			arrival := t0.Add(time.Duration(p.DeliveryMinutes)*time.Minute + plan.Params.MinShelfLifeAtArrival)
			for _, id := range p.UnitIDs {
				u, ok := units[id]
				if !ok {
					t.Fatalf("предложена неизвестная единица %s", id)
				}
				if u.Status != "IN_STOCK" {
					t.Fatalf("предложена единица в статусе %s (ПП РФ № 641, п. 62 а)", u.Status)
				}
				if used[id] {
					t.Fatalf("единица %s предложена дважды", id)
				}
				used[id] = true
				if u.ClinicID != p.SourceClinicID || u.Kind != p.Kind || (groupKind[u.Kind] && u.Group != p.Group) {
					t.Fatalf("единица %s не соответствует предложению %d/%s/%s", id, p.SourceClinicID, p.Kind, p.Group)
				}
				if u.ExpiresAt.Before(arrival) {
					t.Fatalf("единица %s негодна на момент прибытия: %s < %s", id, u.ExpiresAt, arrival)
				}
				volume += u.VolumeML
			}
			if volume != p.VolumeML || len(p.UnitIDs) != p.Units {
				t.Fatalf("предложение %s: объём %d/%d, единиц %d/%d", p.ProposalID, volume, p.VolumeML, len(p.UnitIDs), p.Units)
			}
			seen := map[int64]bool{}
			for _, f := range p.FallbackSources {
				if f == p.SourceClinicID || f == p.TargetClinicID || seen[f] {
					t.Fatalf("запасные источники %v для %d → %d", p.FallbackSources, p.SourceClinicID, p.TargetClinicID)
				}
				seen[f] = true
			}
			if len(p.FallbackSources) > plan.Params.MaxFallbackSources {
				t.Fatalf("запасных источников %d больше %d", len(p.FallbackSources), plan.Params.MaxFallbackSources)
			}
		}
	})
}

func TestPropertyProposalsNotAboveTarget(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := genInput(t)
		plan := buildProp(t, in)
		proposed := map[cellKey]int64{}
		for _, p := range plan.Proposals {
			proposed[cellKey{clinic: p.TargetClinicID, kind: p.Kind, group: p.Group}] += p.VolumeML
		}
		targets := map[cellKey]TargetStock{}
		for _, ts := range plan.Targets {
			targets[cellKey{clinic: ts.ClinicID, kind: ts.Kind, group: ts.Group}] = ts
		}
		for k, v := range proposed {
			ts, ok := targets[k]
			if !ok {
				t.Fatalf("предложение для ячейки %+v без целевого запаса", k)
			}
			if v > ts.TargetML || v > ts.NeedML || v != ts.ProposedML {
				t.Fatalf("ячейка %+v: предложено %d мл при целевом %d, потребности %d, учтено %d", k, v, ts.TargetML, ts.NeedML, ts.ProposedML)
			}
			if ts.TargetML != ts.NormML+ts.BackgroundML+ts.EmergencyML {
				t.Fatalf("ячейка %+v: цель %d не равна норматив + фон + ЧС", k, ts.TargetML)
			}
		}
	})
}

func TestPropertyCoverageOrDeficitRecord(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := genInput(t)
		plan := buildProp(t, in)
		deficits := map[cellKey]Deficit{}
		var uncovered int64
		for _, d := range plan.Deficits {
			k := cellKey{clinic: d.ClinicID, kind: d.Kind, group: d.Group}
			if _, dup := deficits[k]; dup {
				t.Fatalf("две записи дефицита для %+v", k)
			}
			if d.UncoveredML <= 0 {
				t.Fatalf("запись дефицита без непокрытого объёма: %+v", d)
			}
			deficits[k] = d
			uncovered += d.UncoveredML
		}
		for _, ts := range plan.Targets {
			if ts.NeedML == 0 {
				continue
			}
			k := cellKey{clinic: ts.ClinicID, kind: ts.Kind, group: ts.Group}
			d, has := deficits[k]
			coverage := float64(ts.ProposedML) / float64(ts.NeedML)
			if coverage < plan.Params.CoverageThreshold && !has {
				t.Fatalf("ячейка %+v: покрытие %.3f ниже порога без записи дефицита (NFR014)", k, coverage)
			}
			if ts.ProposedML < ts.NeedML && (!has || d.UncoveredML != ts.NeedML-ts.ProposedML) {
				t.Fatalf("ячейка %+v: непокрыто %d мл, запись дефицита %+v", k, ts.NeedML-ts.ProposedML, d)
			}
		}
		if uncovered != plan.NeedML-plan.CoveredML {
			t.Fatalf("сумма дефицита %d, а непокрыто %d", uncovered, plan.NeedML-plan.CoveredML)
		}
		if plan.CoverageRatio < plan.Params.CoverageThreshold && len(plan.Deficits) == 0 {
			t.Fatalf("покрытие плана %.4f ниже порога без записей дефицита", plan.CoverageRatio)
		}
		assertNoUsableUnitLeft(t, in, plan, deficits)
	})
}

// assertNoUsableUnitLeft проверяет полноту жадного подбора (NFR014: при наличии пригодных источников
// непокрытое не остаётся): для каждой ячейки с дефицитом не осталось единицы, которую можно было
// предложить, не нарушив защищённый объём источника и его собственный целевой запас.
func assertNoUsableUnitLeft(t *rapid.T, in Input, plan *Plan, deficits map[cellKey]Deficit) {
	ref := referenceSources(in)
	used := map[uuid.UUID]bool{}
	donatedML := map[kindKey]int64{}
	donatedUnits := map[kindKey]int64{}
	donatedGroup := map[cellKey]int64{}
	for _, p := range plan.Proposals {
		for _, id := range p.UnitIDs {
			used[id] = true
		}
		donatedML[kindKey{clinic: p.SourceClinicID, kind: p.Kind}] += p.VolumeML
		donatedUnits[kindKey{clinic: p.SourceClinicID, kind: p.Kind}] += int64(p.Units)
		donatedGroup[cellKey{clinic: p.SourceClinicID, kind: p.Kind, group: p.Group}] += p.VolumeML
	}
	targets := map[cellKey]TargetStock{}
	for _, ts := range plan.Targets {
		targets[cellKey{clinic: ts.ClinicID, kind: ts.Kind, group: ts.Group}] = ts
	}
	routes := map[[2]int64]int{}
	for _, d := range in.Delivery {
		routes[[2]int64{d.SourceClinicID, d.TargetClinicID}] = d.Minutes
	}
	for k, d := range deficits {
		for _, u := range in.Units {
			group := u.Group
			if u.Kind == "CRYO" {
				group = ""
			}
			if used[u.UnitID] || u.Status != "IN_STOCK" || u.ClinicID == k.clinic || u.Kind != k.kind || group != k.group {
				continue
			}
			minutes, ok := routes[[2]int64{u.ClinicID, k.clinic}]
			if !ok || u.ExpiresAt.Before(t0.Add(time.Duration(minutes)*time.Minute+plan.Params.MinShelfLifeAtArrival)) {
				continue
			}
			if u.VolumeML > d.UncoveredML {
				continue
			}
			sk := kindKey{clinic: u.ClinicID, kind: u.Kind}
			r := ref[sk]
			if r.availML-donatedML[sk]-u.VolumeML < r.protectedML || r.availUnits-donatedUnits[sk]-1 < r.protectedUnits {
				continue
			}
			own := targets[cellKey{clinic: u.ClinicID, kind: u.Kind, group: group}]
			if own.StockML-donatedGroup[cellKey{clinic: u.ClinicID, kind: u.Kind, group: group}]-u.VolumeML < own.TargetML {
				continue
			}
			t.Fatalf("дефицит %+v (%d мл), хотя единица %s клиники %d (%d мл) пригодна", k, d.UncoveredML, u.UnitID, u.ClinicID, u.VolumeML)
		}
	}
}

func TestPropertyPlanIsDeterministicUnderPermutation(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := genInput(t)
		first := buildProp(t, in)
		perm := in
		perm.Units = rapid.Permutation(in.Units).Draw(t, "perm units")
		perm.Norms = rapid.Permutation(in.Norms).Draw(t, "perm norms")
		perm.Background = rapid.Permutation(in.Background).Draw(t, "perm background")
		perm.Obligations = rapid.Permutation(in.Obligations).Draw(t, "perm obligations")
		perm.Delivery = rapid.Permutation(in.Delivery).Draw(t, "perm delivery")
		perm.ClinicShares = rapid.Permutation(in.ClinicShares).Draw(t, "perm shares")
		perm.GroupShares = rapid.Permutation(in.GroupShares).Draw(t, "perm groups")
		if in.Forecast != nil {
			f := *in.Forecast
			f.Lines = rapid.Permutation(in.Forecast.Lines).Draw(t, "perm lines")
			f.Territory = rapid.Permutation(in.Forecast.Territory).Draw(t, "perm territory")
			perm.Forecast = &f
		}
		second := buildProp(t, perm)
		if !reflect.DeepEqual(first, second) {
			t.Fatal("перестановка элементов входа изменила план")
		}
	})
}

func TestPropertyEmergencyNotAboveForecast(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := genInput(t)
		if in.Forecast == nil {
			return
		}
		plan := buildProp(t, in)
		perLine := map[uuid.UUID]int64{}
		for _, a := range plan.Emergency {
			perLine[a.LineID] += a.VolumeML
		}
		for _, l := range in.Forecast.Lines {
			if float64(perLine[l.LineID]) > l.VolumeML {
				t.Fatalf("строка %s: распределено %d мл, в прогнозе %.2f (FR024)", l.LineID, perLine[l.LineID], l.VolumeML)
			}
		}
	})
}
