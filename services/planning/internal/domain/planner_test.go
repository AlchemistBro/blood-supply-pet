package domain

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
)

func route(src, tgt int64, minutes int) DeliveryEstimate {
	return DeliveryEstimate{SourceClinicID: src, TargetClinicID: tgt, Minutes: minutes, EstimatedAt: t0.Add(-30 * time.Minute)}
}

// stockAt — единицы с явным сроком годности.
func stockAt(from, count int, clinic int64, kind, group string, volume int64, expires time.Time, status string) []Unit {
	out := make([]Unit, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, Unit{UnitID: unitN(from + i), ClinicID: clinic, Kind: kind, Group: group, VolumeML: volume, ExpiresAt: expires, Status: status})
	}
	return out
}

// emergencyAt — строка прогноза ЧС с принимающей клиникой.
func emergencyAt(n int, clinic int64, kind, group string, volume float64) ForecastLine {
	return ForecastLine{LineID: unitN(9000 + n), ClinicID: clinic, Kind: kind, Group: group, VolumeML: volume}
}

func build(t *testing.T, in Input) *Plan {
	t.Helper()
	plan, err := NewPlanner(clock.NewMutable(t0)).Build(in)
	if err != nil {
		t.Fatalf("расчёт плана: %v", err)
	}
	return plan
}

func targetOf(t *testing.T, p *Plan, clinic int64, kind, group string) TargetStock {
	t.Helper()
	for _, ts := range p.Targets {
		if ts.ClinicID == clinic && ts.Kind == kind && ts.Group == group {
			return ts
		}
	}
	t.Fatalf("нет целевого запаса %d/%s/%s", clinic, kind, group)
	return TargetStock{}
}

func protectedOf(t *testing.T, p *Plan, clinic int64, kind string) ProtectedVolume {
	t.Helper()
	for _, pv := range p.Protected {
		if pv.ClinicID == clinic && pv.Kind == kind {
			return pv
		}
	}
	t.Fatalf("нет защищённого объёма %d/%s", clinic, kind)
	return ProtectedVolume{}
}

type wantProposal struct {
	source, target int64
	group          string
	units          int
	volume         int64
	minutes        int
	fallback       []int64
}

type wantDeficit struct {
	clinic    int64
	group     string
	uncovered int64
}

func TestSourceSelection(t *testing.T) {
	cases := []struct {
		name      string
		in        func() Input
		proposals []wantProposal
		deficits  []wantDeficit
		coverage  float64
	}{
		{
			name: "быстрее доставка - основной источник, медленный - запасной",
			in: func() Input {
				in := baseInput()
				in.Forecast = &Forecast{Lines: []ForecastLine{emergencyAt(1, 1, "RBC", "O+", 900)}}
				in.Units = append(stock(1, 10, 2, "RBC", "O+", 300, 20, UnitStatusInStock), stock(100, 10, 3, "RBC", "O+", 300, 20, UnitStatusInStock)...)
				in.Delivery = []DeliveryEstimate{route(2, 1, 90), route(3, 1, 30)}
				return in
			},
			proposals: []wantProposal{{source: 3, target: 1, group: "O+", units: 3, volume: 900, minutes: 30, fallback: []int64{2}}},
			coverage:  1,
		},
		{
			name: "единицы на карантине не предлагаются (ПП 641, п. 62 а)",
			in: func() Input {
				in := baseInput()
				in.Forecast = &Forecast{Lines: []ForecastLine{emergencyAt(1, 1, "RBC", "O+", 900)}}
				in.Units = stock(1, 10, 2, "RBC", "O+", 300, 20, UnitStatusQuarantine)
				in.Delivery = []DeliveryEstimate{route(2, 1, 30)}
				return in
			},
			deficits: []wantDeficit{{clinic: 1, group: "O+", uncovered: 900}},
			coverage: 0,
		},
		{
			name: "единица негодна на момент прибытия - берётся дальний источник",
			in: func() Input {
				in := baseInput()
				in.Forecast = &Forecast{Lines: []ForecastLine{emergencyAt(1, 1, "RBC", "O+", 600)}}
				in.Units = append(stockAt(1, 10, 3, "RBC", "O+", 300, t0.Add(20*time.Hour), UnitStatusInStock),
					stock(100, 10, 2, "RBC", "O+", 300, 20, UnitStatusInStock)...)
				in.Delivery = []DeliveryEstimate{route(2, 1, 90), route(3, 1, 30)}
				return in
			},
			proposals: []wantProposal{{source: 2, target: 1, group: "O+", units: 2, volume: 600, minutes: 90}},
			coverage:  1,
		},
		{
			// Норматив источника 2100 делится на A+ 840 и O+ 1260: порог группы O+ позволил бы отдать 1740 мл,
			// защищённый объём вида (2100 мл, 7 ед.) — только 900 мл. Без единиц A+ клиника 2 сама в дефиците по A+.
			name: "защищённый объём источника ограничивает передачу (NFR013)",
			in: func() Input {
				in := baseInput()
				in.Forecast = &Forecast{Lines: []ForecastLine{emergencyAt(1, 1, "RBC", "O+", 3000)}}
				in.Norms = []Norm{{ClinicID: 2, Kind: "RBC", NormML: 2100}}
				in.Units = stock(1, 10, 2, "RBC", "O+", 300, 20, UnitStatusInStock)
				in.Delivery = []DeliveryEstimate{route(2, 1, 30)}
				return in
			},
			proposals: []wantProposal{{source: 2, target: 1, group: "O+", units: 3, volume: 900, minutes: 30}},
			deficits:  []wantDeficit{{clinic: 1, group: "O+", uncovered: 2100}, {clinic: 2, group: "A+", uncovered: 840}},
			coverage:  0.2344,
		},
		{
			// Защищённый объём 2100 + 600 = 2700 мл (9 ед.) оставляет одну единицу; порог группы O+
			// (3000 - 600 - 1260 = 1140 мл) позволил бы три. Без слагаемого обязательств тест падает.
			name: "подтверждённые обязательства источника входят в защищённый объём",
			in: func() Input {
				in := baseInput()
				in.Forecast = &Forecast{Lines: []ForecastLine{emergencyAt(1, 1, "RBC", "O+", 3000)}}
				in.Norms = []Norm{{ClinicID: 2, Kind: "RBC", NormML: 2100}}
				in.Units = stock(1, 10, 2, "RBC", "O+", 300, 20, UnitStatusInStock)
				in.Obligations = []Obligation{{ObligationID: unitN(500), SourceClinicID: 2, TargetClinicID: 4, Kind: "RBC", Group: "O+", VolumeML: 600, Status: ObligationConfirmed}}
				in.Delivery = []DeliveryEstimate{route(2, 1, 30)}
				return in
			},
			proposals: []wantProposal{{source: 2, target: 1, group: "O+", units: 1, volume: 300, minutes: 30}},
			deficits:  []wantDeficit{{clinic: 1, group: "O+", uncovered: 2700}, {clinic: 2, group: "A+", uncovered: 840}},
			coverage:  0.0781,
		},
		{
			name: "источник без оценки доставки не рассматривается",
			in: func() Input {
				in := baseInput()
				in.Forecast = &Forecast{Lines: []ForecastLine{emergencyAt(1, 1, "RBC", "O+", 600)}}
				in.Units = stock(1, 10, 2, "RBC", "O+", 300, 20, UnitStatusInStock)
				return in
			},
			deficits: []wantDeficit{{clinic: 1, group: "O+", uncovered: 600}},
			coverage: 0,
		},
		{
			name: "источник не отдаёт ниже своего целевого запаса по группе",
			in: func() Input {
				in := baseInput()
				in.Forecast = &Forecast{Lines: []ForecastLine{emergencyAt(1, 1, "RBC", "O+", 1500), emergencyAt(2, 2, "RBC", "O+", 2400)}}
				in.Units = stock(1, 10, 2, "RBC", "O+", 300, 20, UnitStatusInStock)
				in.Delivery = []DeliveryEstimate{route(2, 1, 30)}
				return in
			},
			proposals: []wantProposal{{source: 2, target: 1, group: "O+", units: 2, volume: 600, minutes: 30}},
			deficits:  []wantDeficit{{clinic: 1, group: "O+", uncovered: 900}},
			coverage:  0.4,
		},
		{
			name: "входящее подтверждённое обязательство уменьшает потребность",
			in: func() Input {
				in := baseInput()
				in.Forecast = &Forecast{Lines: []ForecastLine{emergencyAt(1, 1, "RBC", "O+", 900)}}
				in.Units = stock(1, 10, 2, "RBC", "O+", 300, 20, UnitStatusInStock)
				in.Obligations = []Obligation{{ObligationID: unitN(500), SourceClinicID: 5, TargetClinicID: 1, Kind: "RBC", Group: "O+", VolumeML: 600, Status: ObligationTransportRequested}}
				in.Delivery = []DeliveryEstimate{route(2, 1, 30)}
				return in
			},
			proposals: []wantProposal{{source: 2, target: 1, group: "O+", units: 1, volume: 300, minutes: 30}},
			coverage:  1,
		},
		{
			name: "единица неделима: остаток меньше единицы уходит в дефицит, предложение не больше потребности",
			in: func() Input {
				in := baseInput()
				in.Forecast = &Forecast{Lines: []ForecastLine{emergencyAt(1, 1, "RBC", "O+", 1000)}}
				in.Units = stock(1, 10, 2, "RBC", "O+", 300, 20, UnitStatusInStock)
				in.Delivery = []DeliveryEstimate{route(2, 1, 30)}
				return in
			},
			proposals: []wantProposal{{source: 2, target: 1, group: "O+", units: 3, volume: 900, minutes: 30}},
			deficits:  []wantDeficit{{clinic: 1, group: "O+", uncovered: 100}},
			coverage:  0.9,
		},
		{
			name: "меньшая единица с поздним сроком дополняет до потребности",
			in: func() Input {
				in := baseInput()
				in.Forecast = &Forecast{Lines: []ForecastLine{emergencyAt(1, 1, "RBC", "O+", 1000)}}
				in.Units = append(stock(1, 4, 2, "RBC", "O+", 300, 20, UnitStatusInStock), stock(50, 1, 2, "RBC", "O+", 100, 30, UnitStatusInStock)...)
				in.Delivery = []DeliveryEstimate{route(2, 1, 30)}
				return in
			},
			proposals: []wantProposal{{source: 2, target: 1, group: "O+", units: 4, volume: 1000, minutes: 30}},
			coverage:  1,
		},
		{
			name: "сценарий ЧС обслуживается раньше норматива",
			in: func() Input {
				in := baseInput()
				in.Forecast = &Forecast{Lines: []ForecastLine{emergencyAt(1, 4, "RBC", "O+", 900)}}
				in.Norms = []Norm{{ClinicID: 1, Kind: "RBC", NormML: 1500}}
				in.Units = stock(1, 3, 2, "RBC", "O+", 300, 20, UnitStatusInStock)
				in.Delivery = []DeliveryEstimate{route(2, 1, 10), route(2, 4, 60)}
				return in
			},
			proposals: []wantProposal{{source: 2, target: 4, group: "O+", units: 3, volume: 900, minutes: 60}},
			deficits:  []wantDeficit{{clinic: 1, group: "A+", uncovered: 600}, {clinic: 1, group: "O+", uncovered: 900}},
			coverage:  0.375,
		},
		{
			name: "вид без группы: единицы любых групп, предложение без группы",
			in: func() Input {
				in := baseInput()
				in.Forecast = &Forecast{Lines: []ForecastLine{emergencyAt(1, 1, "CRYO", "", 40)}}
				in.Units = append(stock(1, 2, 2, "CRYO", "O+", 20, 300, UnitStatusInStock), stock(10, 2, 2, "CRYO", "A+", 20, 300, UnitStatusInStock)...)
				in.Delivery = []DeliveryEstimate{route(2, 1, 30)}
				return in
			},
			proposals: []wantProposal{{source: 2, target: 1, group: "", units: 2, volume: 40, minutes: 30}},
			coverage:  1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := build(t, tc.in())
			got := make([]wantProposal, 0, len(plan.Proposals))
			for _, p := range plan.Proposals {
				if p.Units != len(p.UnitIDs) {
					t.Fatalf("предложение %s: units=%d, а единиц %d", p.ProposalID, p.Units, len(p.UnitIDs))
				}
				got = append(got, wantProposal{source: p.SourceClinicID, target: p.TargetClinicID, group: p.Group,
					units: p.Units, volume: p.VolumeML, minutes: p.DeliveryMinutes, fallback: p.FallbackSources})
			}
			want := tc.proposals
			if want == nil {
				want = []wantProposal{}
			}
			for i := range want {
				if want[i].fallback == nil {
					want[i].fallback = []int64{}
				}
			}
			for i := range got {
				if got[i].fallback == nil {
					got[i].fallback = []int64{}
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("предложения:\n получили %+v\n ожидали  %+v", got, want)
			}
			gotDef := make([]wantDeficit, 0, len(plan.Deficits))
			for _, d := range plan.Deficits {
				if d.Reason != DeficitReasonNoSource {
					t.Fatalf("причина дефицита плана %q, ожидали %q", d.Reason, DeficitReasonNoSource)
				}
				gotDef = append(gotDef, wantDeficit{clinic: d.ClinicID, group: d.Group, uncovered: d.UncoveredML})
			}
			slices.SortFunc(gotDef, func(a, b wantDeficit) int {
				return compareCellKey(cellKey{clinic: a.clinic, group: a.group}, cellKey{clinic: b.clinic, group: b.group})
			})
			wantDef := tc.deficits
			if wantDef == nil {
				wantDef = []wantDeficit{}
			}
			if !reflect.DeepEqual(gotDef, wantDef) {
				t.Fatalf("дефицит:\n получили %+v\n ожидали  %+v", gotDef, wantDef)
			}
			if plan.CoverageRatio != tc.coverage {
				t.Fatalf("покрытие %v, ожидали %v", plan.CoverageRatio, tc.coverage)
			}
			if plan.BelowThreshold != (tc.coverage < plan.Params.CoverageThreshold) {
				t.Fatalf("признак «ниже порога» %v при покрытии %v", plan.BelowThreshold, plan.CoverageRatio)
			}
		})
	}
}

func TestProtectedVolumeRemainsAfterPlan(t *testing.T) {
	in := baseInput()
	in.Forecast = &Forecast{Lines: []ForecastLine{emergencyAt(1, 1, "RBC", "O+", 3000)}}
	in.Norms = []Norm{{ClinicID: 2, Kind: "RBC", NormML: 2100}}
	in.Units = stock(1, 10, 2, "RBC", "O+", 300, 20, UnitStatusInStock)
	in.Delivery = []DeliveryEstimate{route(2, 1, 30)}
	plan := build(t, in)
	pv := protectedOf(t, plan, 2, "RBC")
	if pv.VolumeML != 2100 || pv.VolumeUnits != 7 {
		t.Fatalf("защищённый объём %d мл / %d ед., ожидали 2100 / 7", pv.VolumeML, pv.VolumeUnits)
	}
	if pv.RemainingML != 2100 || pv.RemainingUnits != 7 {
		t.Fatalf("остаток после плана %d мл / %d ед., ожидали 2100 / 7", pv.RemainingML, pv.RemainingUnits)
	}
	ts := targetOf(t, plan, 1, "RBC", "O+")
	if ts.TargetML != 3000 || ts.NeedML != 3000 || ts.ProposedML != 900 || ts.UncoveredML != 2100 {
		t.Fatalf("целевой запас получателя %+v", ts)
	}
}

func TestFallbackSourcesAreLimitedAndRanked(t *testing.T) {
	in := baseInput()
	in.Params.MaxFallbackSources = 2
	in.Forecast = &Forecast{Lines: []ForecastLine{emergencyAt(1, 1, "RBC", "O+", 300)}}
	for i, minutes := range []int{50, 10, 40, 30, 20} {
		clinic := int64(2 + i)
		in.Units = append(in.Units, stock(100*(i+1), 3, clinic, "RBC", "O+", 300, 20, UnitStatusInStock)...)
		in.Delivery = append(in.Delivery, route(clinic, 1, minutes))
	}
	plan := build(t, in)
	if len(plan.Proposals) != 1 {
		t.Fatalf("ожидали одно предложение, получили %d", len(plan.Proposals))
	}
	p := plan.Proposals[0]
	if p.SourceClinicID != 3 || !reflect.DeepEqual(p.FallbackSources, []int64{6, 5}) {
		t.Fatalf("источник %d, запасные %v; ожидали 3 и [6 5]", p.SourceClinicID, p.FallbackSources)
	}
	if p.Explanation.Rank != 1 || p.Explanation.Candidates != 5 {
		t.Fatalf("ранг %d из %d, ожидали 1 из 5", p.Explanation.Rank, p.Explanation.Candidates)
	}
}

func TestPlanIsDeterministic(t *testing.T) {
	in := scenarioInput()
	first := build(t, in)
	second := build(t, in)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("два расчёта на одном входе дали разные планы")
	}
	shuffled := scenarioInput()
	slices.Reverse(shuffled.Units)
	slices.Reverse(shuffled.Delivery)
	slices.Reverse(shuffled.Norms)
	slices.Reverse(shuffled.Background)
	slices.Reverse(shuffled.Obligations)
	slices.Reverse(shuffled.Kinds)
	slices.Reverse(shuffled.GroupShares)
	slices.Reverse(shuffled.ClinicShares)
	slices.Reverse(shuffled.Forecast.Lines)
	slices.Reverse(shuffled.Forecast.Territory)
	third := build(t, shuffled)
	if !reflect.DeepEqual(first, third) {
		t.Fatal("перестановка элементов входа изменила план")
	}
	if len(first.Proposals) == 0 || len(first.Deficits) == 0 {
		t.Fatalf("сценарий должен давать и предложения, и дефицит: %d / %d", len(first.Proposals), len(first.Deficits))
	}
}

// scenarioInput — несколько клиник, оба вида, прогноз с принимающей клиникой и по долям района.
func scenarioInput() Input {
	in := baseInput()
	in.Versions = InputVersions{
		DemandEstimateID: uuid.MustParse("0192f3a1-7b2c-7d4e-8f90-0000000000d1"), DemandVersionNo: 3,
		NormVersion: 17, StockWatermark: 4242, StockSyncedAt: t0.Add(-5 * time.Minute), RefdataVersion: 1,
	}
	in.ClinicShares = []ClinicShare{
		{DistrictID: 1, ClinicID: 1, Share: 0.7}, {DistrictID: 1, ClinicID: 2, Share: 0.3},
		{DistrictID: 2, ClinicID: 3, Share: 1},
	}
	in.Forecast = &Forecast{
		ForecastID: uuid.MustParse("0192f3a1-7b2c-7d4e-8f90-0000000000f1"), ScenarioVersion: "scn-2026-10-20-01",
		Territory: []DistrictWeight{{DistrictID: 1, Weight: 2}, {DistrictID: 2, Weight: 1}},
		Lines: []ForecastLine{
			emergencyAt(1, 0, "RBC", "", 6000),
			emergencyAt(2, 4, "RBC", "A+", 1200),
			emergencyAt(3, 0, "CRYO", "", 200),
		},
	}
	in.Norms = []Norm{{ClinicID: 1, Kind: "RBC", NormML: 900}, {ClinicID: 5, Kind: "RBC", NormML: 1500}, {ClinicID: 5, Kind: "CRYO", NormML: 40}}
	in.Background = []Background{{ClinicID: 5, Kind: "RBC", Group: "O+", VolumeML: 600}, {ClinicID: 6, Kind: "RBC", VolumeML: 300}}
	in.Units = append(in.Units, stock(1, 12, 5, "RBC", "O+", 300, 20, UnitStatusInStock)...)
	in.Units = append(in.Units, stock(100, 8, 5, "RBC", "A+", 300, 3, UnitStatusInStock)...)
	in.Units = append(in.Units, stock(200, 6, 6, "RBC", "O+", 280, 10, UnitStatusInStock)...)
	in.Units = append(in.Units, stock(300, 4, 6, "RBC", "A+", 320, 1, UnitStatusInStock)...)
	in.Units = append(in.Units, stock(400, 5, 6, "RBC", "A+", 300, 15, UnitStatusQuarantine)...)
	in.Units = append(in.Units, stock(500, 9, 5, "CRYO", "O+", 20, 300, UnitStatusInStock)...)
	in.Obligations = []Obligation{
		{ObligationID: unitN(800), SourceClinicID: 5, TargetClinicID: 7, Kind: "RBC", Group: "O+", VolumeML: 600, Status: ObligationConfirmed},
		{ObligationID: unitN(801), SourceClinicID: 6, TargetClinicID: 2, Kind: "RBC", Group: "O+", VolumeML: 280, Status: ObligationConfirmed},
	}
	for _, src := range []int64{5, 6} {
		for _, tgt := range []int64{1, 2, 3, 4} {
			in.Delivery = append(in.Delivery, route(src, tgt, int(10*src+tgt)))
		}
	}
	in.Delivery[0].EstimatedAt = t0.Add(-7 * time.Hour)
	return in
}

func TestPlanCarriesInputVersionsAndExplanation(t *testing.T) {
	in := scenarioInput()
	plan := build(t, in)
	if plan.PlanID != testPlanID || !plan.CreatedAt.Equal(t0) {
		t.Fatalf("идентификатор %s, время %s", plan.PlanID, plan.CreatedAt)
	}
	inputs := plan.Inputs
	if inputs.InputVersions != in.Versions {
		t.Fatalf("версии входов %+v, ожидали %+v", inputs.InputVersions, in.Versions)
	}
	if inputs.ForecastID != in.Forecast.ForecastID || inputs.ScenarioVersion != "scn-2026-10-20-01" {
		t.Fatalf("прогноз в версиях входов: %s %q", inputs.ForecastID, inputs.ScenarioVersion)
	}
	if !inputs.OldestEstimateAt.Equal(t0.Add(-7*time.Hour)) || inputs.AlgorithmVersion == "" {
		t.Fatalf("оценки %s, версия алгоритма %q", inputs.OldestEstimateAt, inputs.AlgorithmVersion)
	}
	for _, p := range plan.Proposals {
		e := p.Explanation
		if e.Chosen.ClinicID != p.SourceClinicID || e.Chosen.DeliveryMinutes != p.DeliveryMinutes {
			t.Fatalf("объяснение не про выбранный источник: %+v", e)
		}
		if e.Rank < 1 || e.Rank > e.Candidates || e.RankingRule == "" {
			t.Fatalf("ранг %d из %d, правило %q", e.Rank, e.Candidates, e.RankingRule)
		}
		if e.RemainingAfterPlanML < e.ProtectedML {
			t.Fatalf("объяснение: остаток %d ниже защищённого %d", e.RemainingAfterPlanML, e.ProtectedML)
		}
		text := e.Text()
		for _, part := range []string{"клиника", "мин", "защищённый объём", "демо"} {
			if !strings.Contains(text, part) {
				t.Fatalf("в тексте объяснения нет %q: %s", part, text)
			}
		}
		if e.Chosen.ClinicID == 5 && p.TargetClinicID == 1 && (!e.Chosen.EstimateStale || !strings.Contains(text, "устарела")) {
			t.Fatalf("устаревшая оценка 5 → 1 не помечена: %s", text)
		}
		if !e.ArrivalAt.Equal(t0.Add(time.Duration(p.DeliveryMinutes) * time.Minute)) {
			t.Fatalf("время прибытия %s", e.ArrivalAt)
		}
		if e.EarliestExpiry.Before(e.ArrivalAt.Add(plan.Params.MinShelfLifeAtArrival)) {
			t.Fatalf("единица истекает %s раньше прибытия с запасом", e.EarliestExpiry)
		}
	}
	for _, d := range plan.Deficits {
		if d.Detail == "" || d.DeficitID == uuid.Nil {
			t.Fatalf("дефицит без пояснения или идентификатора: %+v", d)
		}
	}
}

func TestDeficitDetailNamesExcludedSources(t *testing.T) {
	in := baseInput()
	in.Forecast = &Forecast{Lines: []ForecastLine{emergencyAt(1, 1, "RBC", "O+", 600)}}
	in.Units = stock(1, 10, 2, "RBC", "O+", 300, 20, UnitStatusInStock)
	plan := build(t, in)
	if len(plan.Deficits) != 1 || !strings.Contains(plan.Deficits[0].Detail, "без оценки доставки 1") {
		t.Fatalf("дефицит %+v", plan.Deficits)
	}
}

func TestIdentifiersAreDerivedFromPlan(t *testing.T) {
	in := scenarioInput()
	a := build(t, in)
	in.PlanID = uuid.MustParse("0192f3a1-7b2c-7d4e-8f90-000000000002")
	b := build(t, in)
	if len(a.Proposals) == 0 || a.Proposals[0].ProposalID == b.Proposals[0].ProposalID {
		t.Fatal("идентификаторы предложений разных планов совпали")
	}
	seen := map[uuid.UUID]bool{}
	for _, p := range a.Proposals {
		if seen[p.ProposalID] {
			t.Fatalf("идентификатор предложения %s повторяется", p.ProposalID)
		}
		seen[p.ProposalID] = true
	}
}
