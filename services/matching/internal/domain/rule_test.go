package domain_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/domain"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestRuleValidate(t *testing.T) {
	valid := domain.Rule{
		Code: "R", Version: 1, ComponentKind: "RBC", Priority: 1,
		BasisDocument: "ДЕМО", BasisClause: "1", ValidFrom: day(2026, 1, 1),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("корректное правило отклонено: %v", err)
	}
	cases := map[string]func(r *domain.Rule){
		"пустой код":                  func(r *domain.Rule) { r.Code = "" },
		"версия 0":                    func(r *domain.Rule) { r.Version = 0 },
		"пустой вид":                  func(r *domain.Rule) { r.ComponentKind = "" },
		"приоритет 0":                 func(r *domain.Rule) { r.Priority = 0 },
		"нет основания":               func(r *domain.Rule) { r.BasisDocument = "" },
		"нет пункта основания":        func(r *domain.Rule) { r.BasisClause = "" },
		"нет начала действия":         func(r *domain.Rule) { r.ValidFrom = time.Time{} },
		"конец не позже начала":       func(r *domain.Rule) { r.ValidTo = r.ValidFrom },
		"неизвестная группа заявки":   func(r *domain.Rule) { r.RequestedGroup = "Z+" },
		"неизвестная группа замены":   func(r *domain.Rule) { r.AllowedGroup = "0-" },
		"неизвестный ключ условия":    func(r *domain.Rule) { r.Condition = "WEATHER=RAIN" },
		"условие без значения":        func(r *domain.Rule) { r.Condition = "URGENCY=" },
		"условие без знака равенства": func(r *domain.Rule) { r.Condition = "URGENCY" },
	}
	// Опечатки в значениях условия: правило с ними никогда бы не применилось, ошибка видна только при вводе.
	cases["опечатка в срочности"] = func(r *domain.Rule) { r.Condition = "URGENCY=URGNET" }
	cases["срочность строчными"] = func(r *domain.Rule) { r.Condition = "URGENCY=urgent" }
	cases["опечатка во второй срочности"] = func(r *domain.Rule) { r.Condition = "URGENCY=URGENT|EMERGENSY" }
	cases["опечатка в ограничении"] = func(r *domain.Rule) { r.Condition = "CONSTRAINT=IRADIATED" }
	cases["опечатка в запрете ограничения"] = func(r *domain.Rule) { r.Condition = "NOT_CONSTRAINT=NO_GROUP_SUBST" }
	cases["опечатка во второй части условия"] = func(r *domain.Rule) { r.Condition = "URGENCY=URGENT;CONSTRAINT=IRRADIATE" }
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := valid
			mutate(&r)
			if err := r.Validate(); !errors.Is(err, domain.ErrInvalidRule) {
				t.Fatalf("ожидали ErrInvalidRule, получили %v", err)
			}
		})
	}
}

// На момент заявки берётся версия, действующая в этот момент: начало включительно, конец не включительно.
func TestActiveRulesPicksVersionValidAtMoment(t *testing.T) {
	v1 := domain.Rule{Code: "R", Version: 1, ComponentKind: "RBC", Priority: 2, BasisDocument: "ДЕМО", BasisClause: "1",
		ValidFrom: day(2026, 1, 1), ValidTo: day(2026, 7, 1)}
	v2 := v1
	v2.Version, v2.Priority, v2.ValidFrom, v2.ValidTo = 2, 3, day(2026, 7, 1), time.Time{}
	other := domain.Rule{Code: "Q", Version: 1, ComponentKind: "FFP", Priority: 1, BasisDocument: "ДЕМО", BasisClause: "2",
		ValidFrom: day(2026, 3, 1)}

	cases := []struct {
		name string
		at   time.Time
		want map[string]int64 // код → версия; отсутствие ключа — правило не действует
	}{
		{"до начала всех версий", day(2025, 12, 31), map[string]int64{}},
		{"первая версия", day(2026, 2, 1), map[string]int64{"R": 1}},
		{"обе правила", day(2026, 6, 30), map[string]int64{"R": 1, "Q": 1}},
		{"граница: конец первой версии не включительно", day(2026, 7, 1), map[string]int64{"R": 2, "Q": 1}},
		{"бессрочная вторая версия", day(2030, 1, 1), map[string]int64{"R": 2, "Q": 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.ActiveRules([]domain.Rule{v2, other, v1}, tc.at)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("действующих правил %d, ожидали %d: %+v", len(got), len(tc.want), got)
			}
			for _, r := range got {
				if tc.want[r.Code] != r.Version {
					t.Fatalf("правило %s: версия %d, ожидали %d", r.Code, r.Version, tc.want[r.Code])
				}
			}
			for i := 1; i < len(got); i++ {
				if got[i-1].Code > got[i].Code {
					t.Fatalf("действующие правила не упорядочены по коду: %s, %s", got[i-1].Code, got[i].Code)
				}
			}
		})
	}
}

// Если версии по ошибке ввода перекрываются, побеждает старшая: решение воспроизводимо.
func TestActiveRulesOverlapTakesHighestVersion(t *testing.T) {
	v1 := domain.Rule{Code: "R", Version: 1, ComponentKind: "RBC", Priority: 1, BasisDocument: "ДЕМО", BasisClause: "1",
		ValidFrom: day(2026, 1, 1)}
	v3 := v1
	v3.Version = 3
	got, err := domain.ActiveRules([]domain.Rule{v3, v1}, day(2026, 5, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Version != 3 {
		t.Fatalf("ожидали одну версию 3, получили %+v", got)
	}
}

func TestActiveRulesRejectsInvalidRule(t *testing.T) {
	bad := domain.Rule{Code: "R", Version: 1, ComponentKind: "RBC", Priority: 0, ValidFrom: day(2026, 1, 1)}
	if _, err := domain.ActiveRules([]domain.Rule{bad}, day(2026, 5, 1)); !errors.Is(err, domain.ErrInvalidRule) {
		t.Fatalf("ожидали ErrInvalidRule, получили %v", err)
	}
}

// Закрытые наборы домена совпадают со справочниками libs/refdata (ref_blood_group, ref_urgency).
func TestClosedSetsMatchRefdata(t *testing.T) {
	ref := refdata.MustLoad()
	var groups, urgency []string
	for _, g := range ref.BloodGroups {
		groups = append(groups, g.Code)
	}
	for _, u := range ref.Urgency {
		urgency = append(urgency, u.Code)
	}
	slices.Sort(groups)
	slices.Sort(urgency)
	if !slices.Equal(groups, domain.BloodGroupCodes()) {
		t.Fatalf("группы домена %v, справочника %v", domain.BloodGroupCodes(), groups)
	}
	if !slices.Equal(urgency, domain.UrgencyCodes()) {
		t.Fatalf("срочность домена %v, справочника %v", domain.UrgencyCodes(), urgency)
	}
}

func TestConditionHolds(t *testing.T) {
	req := domain.Request{Urgency: "URGENT", Constraints: []domain.Constraint{{Code: domain.ConstraintIrradiated}}}
	cases := []struct {
		cond string
		want bool
	}{
		{"", true},
		{"ALWAYS", true},
		{"URGENCY=URGENT", true},
		{"URGENCY=PLANNED", false},
		{"URGENCY=URGENT|EMERGENCY", true},
		{"CONSTRAINT=IRRADIATED", true},
		{"CONSTRAINT=NO_GROUP_SUBSTITUTION", false},
		{"NOT_CONSTRAINT=IRRADIATED", false},
		{"NOT_CONSTRAINT=NO_GROUP_SUBSTITUTION", true},
		{"URGENCY=URGENT; CONSTRAINT=IRRADIATED", true},
		{"URGENCY=URGENT;CONSTRAINT=NO_GROUP_SUBSTITUTION", false},
	}
	for _, tc := range cases {
		t.Run(tc.cond, func(t *testing.T) {
			c, err := domain.ParseCondition(tc.cond)
			if err != nil {
				t.Fatal(err)
			}
			if got := c.Holds(req); got != tc.want {
				t.Fatalf("условие %q: %v, ожидали %v", tc.cond, got, tc.want)
			}
		})
	}
}
