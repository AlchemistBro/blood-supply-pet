package domain_test

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/testdata"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/domain"
)

// Общая обстановка: заявка клиники 1 (местный запас), удалённые источники 2 (60 мин), 3 (30 мин)
// и 4 (оценки времени доставки нет). Правила — демо-набор libs/testdata (ДЕМО: синтетические правила, не клинические).
var at = time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)

const (
	local   int64 = 1
	remote2 int64 = 2
	remote3 int64 = 3
	remote4 int64 = 4
)

func demoRules(t *testing.T) []domain.Rule {
	t.Helper()
	var out []domain.Rule
	for _, r := range testdata.DemoMatchingRules() {
		out = append(out, domain.Rule{
			RuleID:         uuid.NewSHA1(uuid.NameSpaceOID, fmt.Appendf(nil, "%s@%d", r.RuleCode, r.VersionNo)),
			Code:           r.RuleCode,
			Version:        r.VersionNo,
			ComponentKind:  r.ComponentKindCode,
			RequestedGroup: r.RequestedGroupCode,
			AllowedGroup:   r.AllowedGroupCode,
			AllowedKind:    r.AllowedKindCode,
			Condition:      r.ApplicationCondition,
			IsException:    r.IsException,
			Priority:       r.RulePriority,
			BasisDocument:  r.BasisDocument,
			BasisClause:    r.BasisClause,
			ValidFrom:      r.ValidFrom,
			ValidTo:        r.ValidTo,
		})
	}
	return out
}

func request(kind, group, urgency string, units int, constraints ...domain.Constraint) domain.Request {
	return domain.Request{
		RequestID:      uuid.MustParse("0192f0a0-0000-7000-8000-0000000000aa"),
		ClinicID:       local,
		ComponentKind:  kind,
		RequestedUnits: units,
		Urgency:        urgency,
		BloodGroup:     group,
		Constraints:    constraints,
		CreatedAt:      at,
	}
}

func unitID(n int) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("0192f0a0-0000-7000-8000-%012d", n))
}

func unit(n int, clinic int64, kind, group string, expiresIn time.Duration) domain.Unit {
	return domain.Unit{
		UnitID:        unitID(n),
		ClinicID:      clinic,
		ComponentKind: kind,
		BloodGroup:    group,
		Phenotype:     "C+c-E+e-K-",
		VolumeML:      300,
		ExpiresAt:     at.Add(expiresIn),
		Status:        domain.StatusInStock,
	}
}

func input(t *testing.T, req domain.Request, units ...domain.Unit) domain.Input {
	t.Helper()
	return domain.Input{
		Request: req,
		At:      at,
		Rules:   demoRules(t),
		Units:   units,
		Delivery: map[int64]domain.DeliveryEstimate{
			remote2: {Minutes: 60, EstimatedAt: at.Add(-10 * time.Minute)},
			remote3: {Minutes: 30, EstimatedAt: at.Add(-10 * time.Minute)},
		},
		StockWatermark: 4242,
		StockSyncedAt:  at.Add(-5 * time.Minute),
		Policy:         domain.DemoPolicy(),
	}
}

func mustMatch(t *testing.T, in domain.Input) domain.Result {
	t.Helper()
	res, err := domain.Match(in)
	if err != nil {
		t.Fatalf("подбор: %v", err)
	}
	return res
}

type variantView struct {
	clinic    int64
	group     string
	rule      string
	preferred bool
}

func views(res domain.Result) []variantView {
	out := make([]variantView, 0, len(res.Variants))
	for _, v := range res.Variants {
		out = append(out, variantView{clinic: v.SourceClinicID, group: v.BloodGroup, rule: v.Rule.Code, preferred: v.IsPreferred})
	}
	return out
}

const day24 = 24 * time.Hour

// FR030: предпочтительный вариант (точное соответствие заявке) стоит выше любой замены, даже если замена
// ближе, свежее или покрывает заявку полностью.
func TestPreferredVariantRanksAboveSubstitution(t *testing.T) {
	cases := []struct {
		name  string
		units []domain.Unit
	}{
		{
			name: "замена местная, предпочтительный удалённый",
			units: []domain.Unit{
				unit(1, remote2, "RBC", "A+", 10*day24),
				unit(2, local, "RBC", "A-", 10*day24),
				unit(3, local, "RBC", "A-", 10*day24),
			},
		},
		{
			name: "замена истекает раньше (FEFO), предпочтительный позже",
			units: []domain.Unit{
				unit(1, local, "RBC", "A+", 20*day24),
				unit(2, remote3, "RBC", "A-", 2*day24),
				unit(3, remote3, "RBC", "A-", 2*day24),
			},
		},
		{
			name: "замена покрывает заявку, предпочтительного не хватает",
			units: []domain.Unit{
				unit(1, remote2, "RBC", "A+", 5*day24),
				unit(2, remote3, "RBC", "A-", 5*day24),
				unit(3, remote3, "RBC", "A-", 5*day24),
				unit(4, remote3, "RBC", "A-", 5*day24),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := mustMatch(t, input(t, request("RBC", "A+", "URGENT", 2), tc.units...))
			got := views(res)
			if len(got) != 2 {
				t.Fatalf("вариантов %d, ожидали 2: %+v", len(got), got)
			}
			if !got[0].preferred || got[0].group != "A+" || got[0].rule != "DEMO_RBC_IDENTICAL" {
				t.Fatalf("первым должен быть предпочтительный A+ по DEMO_RBC_IDENTICAL: %+v", got)
			}
			if got[1].preferred || got[1].group != "A-" || got[1].rule != "DEMO_RBC_A_NEG_FOR_A_POS" {
				t.Fatalf("вторым должна быть замена A- по DEMO_RBC_A_NEG_FOR_A_POS: %+v", got)
			}
			if res.Variants[0].Rank != 1 || res.Variants[1].Rank != 2 || res.Deficit != nil {
				t.Fatalf("ранги %d, %d, дефицит %+v", res.Variants[0].Rank, res.Variants[1].Rank, res.Deficit)
			}
		})
	}
}

// FR030: замены предлагаются только при отсутствии или нехватке предпочтительного варианта.
func TestSubstitutesOfferedOnlyWhenPreferredInsufficient(t *testing.T) {
	units := []domain.Unit{
		unit(1, remote2, "RBC", "A+", 5*day24),
		unit(2, remote2, "RBC", "A+", 5*day24),
		unit(3, local, "RBC", "A-", 5*day24),
	}
	res := mustMatch(t, input(t, request("RBC", "A+", "URGENT", 2), units...))
	if got := views(res); len(got) != 1 || got[0].group != "A+" {
		t.Fatalf("предпочтительный покрывает заявку: ожидали только A+, получили %+v", got)
	}

	in := input(t, request("RBC", "A+", "URGENT", 2), units...)
	in.Policy.OfferSubstitutesWhenPreferredCovers = true
	if got := views(mustMatch(t, in)); len(got) != 2 || got[0].group != "A+" || got[1].group != "A-" {
		t.Fatalf("с параметром показа замен ожидали A+, затем A-, получили %+v", got)
	}
}

// Исключение правила запрещает замену: демо-исключение DEMO_FFP_AB_PLANNED_EXC запрещает плазму AB+
// для плановой заявки, хотя DEMO_FFP_AB её разрешает.
func TestExceptionForbidsSubstitution(t *testing.T) {
	cases := []struct {
		urgency     string
		wantDeficit bool
	}{
		{"PLANNED", true},
		{"URGENT", false},
		{"EMERGENCY", false},
	}
	for _, tc := range cases {
		t.Run(tc.urgency, func(t *testing.T) {
			res := mustMatch(t, input(t, request("FFP", "A+", tc.urgency, 1), unit(1, remote2, "FFP", "AB+", 300*day24)))
			if tc.wantDeficit {
				if len(res.Variants) != 0 || res.Deficit == nil || res.Deficit.Reason != domain.DeficitNoAllowedVariant {
					t.Fatalf("исключение должно запретить замену: %+v", views(res))
				}
				if !strings.Contains(res.Deficit.Explanation, "DEMO_FFP_AB_PLANNED_EXC") {
					t.Fatalf("объяснение дефицита не называет исключение: %s", res.Deficit.Explanation)
				}
				return
			}
			got := views(res)
			if len(got) != 1 || got[0].group != "AB+" || got[0].rule != "DEMO_FFP_AB" {
				t.Fatalf("ожидали замену AB+ по DEMO_FFP_AB, получили %+v", got)
			}
		})
	}
}

// Исключение запрещает только замену: точное соответствие заявке AB+ остаётся допустимым.
func TestExceptionDoesNotForbidPreferred(t *testing.T) {
	res := mustMatch(t, input(t, request("FFP", "AB+", "PLANNED", 1), unit(1, remote2, "FFP", "AB+", 300*day24)))
	if got := views(res); len(got) != 1 || !got[0].preferred || got[0].rule != "DEMO_FFP_IDENTICAL" {
		t.Fatalf("ожидали предпочтительный AB+ по DEMO_FFP_IDENTICAL, получили %+v", got)
	}
}

// Проверка «тест действительно проверяет» этапа 6 (docs/tasks/06-matching.md).
// Ловит правило: применение ограничения NO_GROUP_SUBSTITUTION в match.go (permittedTargets удаляет
// разрешённые правилами замены группы). Если это применение убрать, подбор предложит A- по
// DEMO_RBC_A_NEG_FOR_A_POS и O- по DEMO_RBC_O_NEG, и тест упадёт: вместо дефицита появятся варианты.
func TestNoGroupSubstitutionConstraintIsEnforced(t *testing.T) {
	units := []domain.Unit{
		unit(1, local, "RBC", "A-", 10*day24),
		unit(2, remote3, "RBC", "O-", 10*day24),
		unit(3, remote3, "RBC", "O-", 10*day24),
	}
	// Без ограничения замены допустимы: обстановка действительно содержит запас для замены.
	free := mustMatch(t, input(t, request("RBC", "A+", "EMERGENCY", 1), units...))
	if len(free.Variants) != 2 {
		t.Fatalf("без ограничения ожидали замены A- и O-, получили %+v", views(free))
	}

	res := mustMatch(t, input(t,
		request("RBC", "A+", "EMERGENCY", 1, domain.Constraint{Code: domain.ConstraintNoGroupSubstitution}), units...))
	if len(res.Variants) != 0 {
		t.Fatalf("NO_GROUP_SUBSTITUTION нарушено: предложены %+v", views(res))
	}
	if res.Deficit == nil || res.Deficit.Reason != domain.DeficitNoAllowedVariant || res.Deficit.UncoveredUnits != 1 {
		t.Fatalf("ожидали дефицит NO_ALLOWED_VARIANT на 1 ед., получили %+v", res.Deficit)
	}
	if !strings.Contains(res.Deficit.Explanation, domain.ConstraintNoGroupSubstitution) {
		t.Fatalf("объяснение дефицита не называет ограничение: %s", res.Deficit.Explanation)
	}
}

// Ограничение заявки побеждает нехватку запаса: лучше дефицит, чем вариант с нарушением (FR030).
func TestConstraintBeatsShortage(t *testing.T) {
	irradiated := unit(10, remote2, "RBC", "A+", 10*day24)
	irradiated.Attributes = []string{domain.ConstraintIrradiated}
	kPositive := unit(11, remote2, "RBC", "A+", 10*day24)
	kPositive.Phenotype = "C+c-E+e-K+"
	noPhenotype := unit(12, remote3, "RBC", "A+", 10*day24)
	noPhenotype.Phenotype = ""

	cases := []struct {
		name      string
		req       domain.Request
		units     []domain.Unit
		wantUnits []uuid.UUID // nil — ожидаем дефицит
	}{
		{
			name:  "NO_GROUP_SUBSTITUTION: есть только замены",
			req:   request("RBC", "A+", "EMERGENCY", 2, domain.Constraint{Code: domain.ConstraintNoGroupSubstitution}),
			units: []domain.Unit{unit(1, local, "RBC", "A-", 10*day24), unit(2, local, "RBC", "O-", 10*day24)},
		},
		{
			name:  "фенотип K-: единицы K+ и без сведений о фенотипе",
			req:   withPhenotype(request("RBC", "A+", "URGENT", 1), "K-"),
			units: []domain.Unit{kPositive, noPhenotype},
		},
		{
			name:      "фенотип K-: подходящая единица выбирается, неподходящие нет",
			req:       withPhenotype(request("RBC", "A+", "URGENT", 1), "K-"),
			units:     []domain.Unit{kPositive, noPhenotype, unit(13, remote3, "RBC", "A+", 10*day24)},
			wantUnits: []uuid.UUID{unitID(13)},
		},
		{
			name:  "IRRADIATED: у единиц нет признака",
			req:   request("RBC", "A+", "URGENT", 1, domain.Constraint{Code: domain.ConstraintIrradiated}),
			units: []domain.Unit{unit(1, local, "RBC", "A+", 10*day24), unit(2, remote3, "RBC", "A+", 10*day24)},
		},
		{
			name:      "IRRADIATED: единица с признаком",
			req:       request("RBC", "A+", "URGENT", 1, domain.Constraint{Code: domain.ConstraintIrradiated}),
			units:     []domain.Unit{unit(1, local, "RBC", "A+", 10*day24), irradiated},
			wantUnits: []uuid.UUID{unitID(10)},
		},
		{
			name: "MIN_REMAINING_HOURS 72: местной единице остаётся 48 ч",
			req: request("RBC", "A+", "URGENT", 1,
				domain.Constraint{Code: domain.ConstraintMinRemainingHours, Value: "72"}),
			units: []domain.Unit{unit(1, local, "RBC", "A+", 48*time.Hour)},
		},
		{
			name:  "вид без действующего правила",
			req:   request("GRAN", "A+", "EMERGENCY", 1),
			units: []domain.Unit{unit(1, local, "GRAN", "A+", 10*day24)},
		},
		{
			name:  "другой вид без правила замены вида",
			req:   request("PLT", "A+", "EMERGENCY", 1),
			units: []domain.Unit{unit(1, local, "RBC", "A+", 10*day24)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := mustMatch(t, input(t, tc.req, tc.units...))
			if tc.wantUnits == nil {
				if len(res.Variants) != 0 {
					t.Fatalf("ограничение нарушено: предложены %+v", views(res))
				}
				if res.Deficit == nil || res.Deficit.Reason != domain.DeficitNoAllowedVariant ||
					res.Deficit.UncoveredUnits != tc.req.RequestedUnits {
					t.Fatalf("ожидали дефицит NO_ALLOWED_VARIANT, получили %+v", res.Deficit)
				}
				return
			}
			if res.Deficit != nil || len(res.Variants) != 1 || !slices.Equal(res.Variants[0].UnitIDs, tc.wantUnits) {
				t.Fatalf("ожидали один вариант с единицами %v, получили %+v (дефицит %+v)", tc.wantUnits, res.Variants, res.Deficit)
			}
		})
	}
}

func withPhenotype(r domain.Request, p string) domain.Request {
	r.Phenotype = p
	return r
}

// ПП РФ № 641, п. 62 а: предлагаются только единицы в наличии; карантин и выбывшие не предлагаются никогда.
func TestOnlyInStockUnitsAreOffered(t *testing.T) {
	statuses := []string{"QUARANTINE", "ISSUED", "DISCARDED", "WRITTEN_OFF", "TRANSFERRED", ""}
	for _, st := range statuses {
		t.Run("только "+st, func(t *testing.T) {
			u := unit(1, local, "RBC", "A+", 10*day24)
			u.Status = st
			res := mustMatch(t, input(t, request("RBC", "A+", "URGENT", 1), u))
			if len(res.Variants) != 0 || res.Deficit == nil {
				t.Fatalf("единица в статусе %q предложена: %+v", st, views(res))
			}
		})
	}
	t.Run("смешанный запас", func(t *testing.T) {
		var units []domain.Unit
		for i, st := range statuses {
			u := unit(i+1, local, "RBC", "A+", time.Duration(i+1)*day24) // недоступные истекают раньше: FEFO выбрал бы их
			u.Status = st
			units = append(units, u)
		}
		units = append(units, unit(100, local, "RBC", "A+", 30*day24))
		res := mustMatch(t, input(t, request("RBC", "A+", "URGENT", 3), units...))
		if len(res.Variants) != 1 {
			t.Fatalf("ожидали один вариант, получили %+v", views(res))
		}
		v := res.Variants[0]
		if v.AvailableUnits != 1 || !slices.Equal(v.UnitIDs, []uuid.UUID{unitID(100)}) || v.CoversRequest {
			t.Fatalf("в варианте должна быть только единица IN_STOCK: доступно %d, единицы %v, покрывает %v",
				v.AvailableUnits, v.UnitIDs, v.CoversRequest)
		}
	})
}

// Единица, срок годности которой истекает до прибытия (или ровно в момент прибытия), не предлагается.
func TestUnitExpiringBeforeArrivalIsNotOffered(t *testing.T) {
	cases := []struct {
		name      string
		clinic    int64
		expiresIn time.Duration
		offered   bool
	}{
		{"удалённый 60 мин, истекает через 59 мин", remote2, 59 * time.Minute, false},
		{"удалённый 60 мин, истекает в момент прибытия", remote2, 60 * time.Minute, false},
		{"удалённый 60 мин, истекает через 61 мин", remote2, 61 * time.Minute, true},
		{"местный, истекает через минуту", local, time.Minute, true},
		{"местный, уже истёк", local, -time.Minute, false},
		// Оценки нет: для проверки годности берётся демо-допущение (DemoPolicy, 240 мин).
		{"без оценки, истекает через 3 ч", remote4, 3 * time.Hour, false},
		{"без оценки, истекает через 5 ч", remote4, 5 * time.Hour, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := mustMatch(t, input(t, request("RBC", "A+", "URGENT", 1), unit(1, tc.clinic, "RBC", "A+", tc.expiresIn)))
			if got := len(res.Variants) == 1; got != tc.offered {
				t.Fatalf("предложена: %v, ожидали %v", got, tc.offered)
			}
		})
	}
}

// FR034: порядок факторов — приоритет правила → годность на момент прибытия (FEFO, шаг 24 ч) →
// время получения (оценки нет — последним) → расход защищённого запаса источника → номер клиники.
func TestRankingFactors(t *testing.T) {
	cases := []struct {
		name   string
		req    domain.Request
		units  []domain.Unit
		mutate func(in *domain.Input)
		want   []int64
	}{
		{
			name:  "приоритет правила важнее годности",
			req:   request("RBC", "A+", "EMERGENCY", 1),
			units: []domain.Unit{unit(1, remote2, "RBC", "O-", 2*day24), unit(2, remote3, "RBC", "A-", 10*day24)},
			want:  []int64{remote3, remote2},
		},
		{
			name:  "годность (FEFO) важнее времени получения",
			req:   request("RBC", "A+", "URGENT", 1),
			units: []domain.Unit{unit(1, remote2, "RBC", "A+", 2*day24), unit(2, remote3, "RBC", "A+", 5*day24)},
			want:  []int64{remote2, remote3},
		},
		{
			name:  "в одном шаге годности решает время получения",
			req:   request("RBC", "A+", "URGENT", 1),
			units: []domain.Unit{unit(1, remote2, "RBC", "A+", 30*time.Hour), unit(2, remote3, "RBC", "A+", 40*time.Hour)},
			want:  []int64{remote3, remote2},
		},
		{
			name:  "местный вариант (0 мин) раньше удалённого",
			req:   request("RBC", "A+", "URGENT", 1),
			units: []domain.Unit{unit(1, remote3, "RBC", "A+", 30*time.Hour), unit(2, local, "RBC", "A+", 40*time.Hour)},
			want:  []int64{local, remote3},
		},
		{
			name:  "оценки нет — после известной оценки",
			req:   request("RBC", "A+", "URGENT", 1),
			units: []domain.Unit{unit(1, remote4, "RBC", "A+", 30*time.Hour), unit(2, remote2, "RBC", "A+", 40*time.Hour)},
			want:  []int64{remote2, remote4},
		},
		{
			name:  "меньше расход защищённого запаса — выше",
			req:   request("RBC", "A+", "URGENT", 1),
			units: []domain.Unit{unit(1, remote3, "RBC", "A+", 30*time.Hour), unit(2, remote2, "RBC", "A+", 30*time.Hour)},
			mutate: func(in *domain.Input) {
				in.Delivery[remote2] = domain.DeliveryEstimate{Minutes: 30, EstimatedAt: at}
				in.Stock = map[domain.StockKey]domain.SourceStock{
					{ClinicID: remote2, ComponentKind: "RBC"}: {AvailableVolumeML: 3000, ProtectedVolumeML: 0},
					{ClinicID: remote3, ComponentKind: "RBC"}: {AvailableVolumeML: 600, ProtectedVolumeML: 600},
				}
			},
			want: []int64{remote2, remote3},
		},
		{
			name:  "нет сведений о защищённом объёме — после известных",
			req:   request("RBC", "A+", "URGENT", 1),
			units: []domain.Unit{unit(1, remote2, "RBC", "A+", 30*time.Hour), unit(2, remote3, "RBC", "A+", 30*time.Hour)},
			mutate: func(in *domain.Input) {
				in.Delivery[remote2] = domain.DeliveryEstimate{Minutes: 30, EstimatedAt: at}
				in.Stock = map[domain.StockKey]domain.SourceStock{
					{ClinicID: remote3, ComponentKind: "RBC"}: {AvailableVolumeML: 3000, ProtectedVolumeML: 0},
				}
			},
			want: []int64{remote3, remote2},
		},
		{
			name:  "равные факторы — по номеру клиники",
			req:   request("RBC", "A+", "URGENT", 1),
			units: []domain.Unit{unit(1, remote3, "RBC", "A+", 30*time.Hour), unit(2, remote2, "RBC", "A+", 30*time.Hour)},
			mutate: func(in *domain.Input) {
				in.Delivery[remote2] = domain.DeliveryEstimate{Minutes: 30, EstimatedAt: at}
			},
			want: []int64{remote2, remote3},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := input(t, tc.req, tc.units...)
			if tc.mutate != nil {
				tc.mutate(&in)
			}
			res := mustMatch(t, in)
			got := make([]int64, 0, len(res.Variants))
			for i, v := range res.Variants {
				got = append(got, v.SourceClinicID)
				if v.Rank != i+1 {
					t.Fatalf("ранг %d на позиции %d", v.Rank, i)
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("порядок источников %v, ожидали %v", got, tc.want)
			}
		})
	}
}

// Ранжирование детерминировано: порядок входных единиц и правил не меняет результат.
func TestRankingIsDeterministic(t *testing.T) {
	groups := []string{"A+", "A-", "O-"}
	var units []domain.Unit
	for i := range 90 {
		clinic := int64(1 + i%6)
		u := unit(i+1, clinic, "RBC", groups[(i/6)%len(groups)], time.Duration(20+(i*7)%200)*time.Hour)
		u.VolumeML = int64(250 + (i*13)%80)
		if i%11 == 0 {
			u.Status = "QUARANTINE"
		}
		units = append(units, u)
	}
	base := input(t, request("RBC", "A+", "EMERGENCY", 3), units...)
	base.Policy.OfferSubstitutesWhenPreferredCovers = true
	base.Delivery[5] = domain.DeliveryEstimate{Minutes: 45, EstimatedAt: at}
	base.Stock = map[domain.StockKey]domain.SourceStock{
		{ClinicID: remote2, ComponentKind: "RBC"}: {AvailableVolumeML: 5000, ProtectedVolumeML: 4800},
		{ClinicID: remote3, ComponentKind: "RBC"}: {AvailableVolumeML: 5000, ProtectedVolumeML: 1000},
	}
	want := mustMatch(t, base)
	if len(want.Variants) < 12 {
		t.Fatalf("обстановка слишком бедная для проверки: %d вариантов", len(want.Variants))
	}
	for seed := range uint64(5) {
		rng := rand.New(rand.NewPCG(seed, seed+1))
		in := base
		in.Units = slices.Clone(base.Units)
		in.Rules = slices.Clone(base.Rules)
		rng.Shuffle(len(in.Units), func(i, j int) { in.Units[i], in.Units[j] = in.Units[j], in.Units[i] })
		rng.Shuffle(len(in.Rules), func(i, j int) { in.Rules[i], in.Rules[j] = in.Rules[j], in.Rules[i] })
		if got := mustMatch(t, in); !reflect.DeepEqual(got, want) {
			t.Fatalf("перестановка %d входов изменила результат подбора", seed)
		}
	}
}

// FR031: в варианте конкретные единицы по FEFO, доступное количество и признак покрытия заявки.
func TestVariantUnitsAreSelectedFEFO(t *testing.T) {
	units := []domain.Unit{
		unit(5, local, "RBC", "A+", 9*day24),
		unit(4, local, "RBC", "A+", 3*day24),
		unit(3, local, "RBC", "A+", 7*day24),
		unit(2, local, "RBC", "A+", 3*day24),
		unit(1, local, "RBC", "A+", 12*day24),
	}
	res := mustMatch(t, input(t, request("RBC", "A+", "URGENT", 3), units...))
	if len(res.Variants) != 1 {
		t.Fatalf("ожидали один вариант, получили %+v", views(res))
	}
	v := res.Variants[0]
	// Две единицы истекают одновременно: порядок между ними — по идентификатору.
	if want := []uuid.UUID{unitID(2), unitID(4), unitID(3)}; !slices.Equal(v.UnitIDs, want) {
		t.Fatalf("единицы %v, ожидали %v", v.UnitIDs, want)
	}
	if v.AvailableUnits != 5 || !v.CoversRequest || !v.IsLocal {
		t.Fatalf("доступно %d, покрывает %v, местный %v", v.AvailableUnits, v.CoversRequest, v.IsLocal)
	}
	if v.Delivery.Status != domain.DeliveryLocal || v.Delivery.Minutes != 0 || !v.ArrivalAt.Equal(at) {
		t.Fatalf("местный вариант: %+v, прибытие %v", v.Delivery, v.ArrivalAt)
	}
	if !v.EarliestExpiresAt.Equal(at.Add(3 * day24)) {
		t.Fatalf("ближайшая годность %v", v.EarliestExpiresAt)
	}
	if v.IsDeficitStockUsed || v.Protected.Known {
		t.Fatalf("местный вариант не расходует защищённый запас источника: %+v", v.Protected)
	}
}

// FR032, FR033: время получения с источником оценки; остаток источника после передачи и расход защищённого запаса.
func TestRemoteVariantDeliveryAndProtectedStock(t *testing.T) {
	in := input(t, request("RBC", "A+", "URGENT", 2),
		unit(1, remote2, "RBC", "A+", 5*day24), unit(2, remote2, "RBC", "A+", 6*day24), unit(3, remote4, "RBC", "A+", 5*day24))
	in.Stock = map[domain.StockKey]domain.SourceStock{
		{ClinicID: remote2, ComponentKind: "RBC"}: {AvailableVolumeML: 3000, ProtectedVolumeML: 2500},
	}
	res := mustMatch(t, in)
	if len(res.Variants) != 2 {
		t.Fatalf("ожидали два варианта, получили %+v", views(res))
	}
	v := res.Variants[0]
	if v.SourceClinicID != remote2 || v.Delivery.Status != domain.DeliveryEstimated || v.Delivery.Minutes != 60 ||
		!v.Delivery.EstimatedAt.Equal(at.Add(-10*time.Minute)) || !v.ArrivalAt.Equal(at.Add(time.Hour)) {
		t.Fatalf("время получения удалённого варианта: %+v, прибытие %v", v.Delivery, v.ArrivalAt)
	}
	p := v.Protected
	if !p.Known || p.RemainingAfterML != 2400 || p.ConsumedML != 100 || !v.IsDeficitStockUsed {
		t.Fatalf("защищённый запас: %+v, расходует %v", p, v.IsDeficitStockUsed)
	}
	if !strings.Contains(v.Explanation, "2400 мл") || !strings.Contains(v.Explanation, "2500 мл") {
		t.Fatalf("объяснение не показывает остаток источника после передачи: %s", v.Explanation)
	}
	u := res.Variants[1]
	if u.SourceClinicID != remote4 || u.Delivery.Status != domain.DeliveryUnknown || u.Protected.Known || u.IsDeficitStockUsed {
		t.Fatalf("вариант без оценки и без сведений о запасе: %+v", u)
	}
	if !strings.Contains(u.Explanation, "оценки нет") {
		t.Fatalf("объяснение не говорит, что оценки нет: %s", u.Explanation)
	}
}

// FR047, FR035: решение ссылается на версию правила, действовавшую на момент заявки, а не на момент подбора.
func TestRulesVersionTakenAtRequestMoment(t *testing.T) {
	oNeg := unit(1, remote3, "RBC", "O-", 10*day24)
	cases := []struct {
		name        string
		created     time.Time
		wantVersion int64 // 0 — замена O- не допускается
	}{
		{"заявка в мае: версия 1 разрешает URGENT", day(2026, 5, 1), 1},
		{"заявка в октябре: версия 2 только для EMERGENCY", day(2026, 10, 1), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := request("RBC", "A+", "URGENT", 1)
			req.CreatedAt = tc.created
			res := mustMatch(t, input(t, req, oNeg))
			if !res.RulesAt.Equal(tc.created) {
				t.Fatalf("правила взяты на %v, ожидали момент заявки %v", res.RulesAt, tc.created)
			}
			if tc.wantVersion == 0 {
				if len(res.Variants) != 0 {
					t.Fatalf("замена O- не должна предлагаться: %+v", views(res))
				}
				return
			}
			if len(res.Variants) != 1 || res.Variants[0].Rule.Code != "DEMO_RBC_O_NEG" || res.Variants[0].Rule.Version != tc.wantVersion {
				t.Fatalf("ожидали DEMO_RBC_O_NEG v%d, получили %+v", tc.wantVersion, res.Variants)
			}
		})
	}
}

// NFR009: объяснение содержит правило, его версию, основание, версии входов и пометку «демо»;
// система не утверждает совместимость и не раскрывает фенотип.
func TestExplanationHasRuleVersionAndDemoMark(t *testing.T) {
	req := withPhenotype(request("RBC", "A+", "EMERGENCY", 2), "K-")
	in := input(t, req,
		unit(1, local, "RBC", "A+", 5*day24),
		unit(2, remote2, "RBC", "A-", 5*day24),
		unit(3, remote3, "RBC", "O-", 5*day24))
	res := mustMatch(t, in)
	if len(res.Variants) != 3 {
		t.Fatalf("ожидали три варианта, получили %+v", views(res))
	}
	for _, v := range res.Variants {
		e := v.Explanation
		for _, want := range []string{
			"ДЕМО",
			v.Rule.Code,
			fmt.Sprintf("версии %d", v.Rule.Version),
			testdata.DemoRulesBasisDocument,
			"4242",
			"DEMO_RBC_O_NEG v2",
			fmt.Sprintf("Место %d из 3", v.Rank),
		} {
			if !strings.Contains(e, want) {
				t.Fatalf("вариант %d: в объяснении нет %q:\n%s", v.Rank, want, e)
			}
		}
		if strings.Contains(strings.ToLower(e), "совмест") {
			t.Fatalf("объяснение утверждает совместимость:\n%s", e)
		}
		if strings.Contains(e, "K-") {
			t.Fatalf("объяснение раскрывает фенотип:\n%s", e)
		}
		if v.Rank > 1 && !strings.Contains(e, fmt.Sprintf("ниже места %d", v.Rank-1)) {
			t.Fatalf("вариант %d: объяснение не говорит, почему он ниже предыдущего:\n%s", v.Rank, e)
		}
	}
	if len(res.RulesUsed) == 0 || res.StockWatermark != 4242 || !res.StockSyncedAt.Equal(at.Add(-5*time.Minute)) {
		t.Fatalf("версии входов не сохранены: правила %d, водяной знак %d", len(res.RulesUsed), res.StockWatermark)
	}

	deficit := mustMatch(t, input(t, request("RBC", "A+", "URGENT", 1)))
	if deficit.Deficit == nil {
		t.Fatal("ожидали дефицит при пустом запасе")
	}
	for _, want := range []string{"ДЕМО", "DEMO_RBC_IDENTICAL v1", "4242"} {
		if !strings.Contains(deficit.Deficit.Explanation, want) {
			t.Fatalf("в объяснении дефицита нет %q:\n%s", want, deficit.Deficit.Explanation)
		}
	}
}

// ADR-0011: спецификация замены из варианта — группа варианта, требования фенотипа заявки,
// минимальная годность на момент прибытия и версия правила.
func TestReplacementSpec(t *testing.T) {
	req := withPhenotype(request("RBC", "A+", "URGENT", 1,
		domain.Constraint{Code: domain.ConstraintMinRemainingHours, Value: "12"}), "K-")
	res := mustMatch(t, input(t, req, unit(1, remote2, "RBC", "A-", 5*day24)))
	if len(res.Variants) != 1 {
		t.Fatalf("ожидали один вариант, получили %+v", views(res))
	}
	s := res.Variants[0].Spec
	want := domain.ReplacementSpec{
		ComponentKind: "RBC", BloodGroup: "A-", Phenotype: "K-",
		MinExpiresAt: at.Add(time.Hour + 12*time.Hour),
		RuleCode:     "DEMO_RBC_A_NEG_FOR_A_POS", RuleVersion: 1,
	}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("спецификация %+v, ожидали %+v", s, want)
	}
}

// Замена вида разрешается только правилом с allowed_kind_code.
func TestKindSubstitutionNeedsRule(t *testing.T) {
	rules := []domain.Rule{
		{Code: "T_KIND", Version: 1, ComponentKind: "PLT", AllowedKind: "PLTA", Priority: 2,
			BasisDocument: "ДЕМО", BasisClause: "т", ValidFrom: day(2026, 1, 1)},
	}
	in := input(t, request("PLT", "A+", "URGENT", 1), unit(1, remote3, "PLTA", "A+", 3*day24))
	in.Rules = rules
	res := mustMatch(t, in)
	if len(res.Variants) != 1 || res.Variants[0].ComponentKind != "PLTA" || res.Variants[0].IsPreferred {
		t.Fatalf("ожидали замену вида PLTA, получили %+v", res.Variants)
	}
	in.Rules = nil
	if res := mustMatch(t, in); len(res.Variants) != 0 || res.Deficit == nil {
		t.Fatalf("без правила замена вида предложена: %+v", res.Variants)
	}
}

func TestMatchRejectsInvalidInput(t *testing.T) {
	bad := input(t, request("RBC", "A+", "URGENT", 0))
	if _, err := domain.Match(bad); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("ожидали ErrInvalidRequest, получили %v", err)
	}
	noTime := input(t, request("RBC", "A+", "URGENT", 1))
	noTime.At = time.Time{}
	if _, err := domain.Match(noTime); err == nil {
		t.Fatal("подбор без момента времени принят")
	}
	badRule := input(t, request("RBC", "A+", "URGENT", 1))
	badRule.Rules = append(badRule.Rules, domain.Rule{Code: "BROKEN"})
	if _, err := domain.Match(badRule); !errors.Is(err, domain.ErrInvalidRule) {
		t.Fatalf("ожидали ErrInvalidRule, получили %v", err)
	}
}
