package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/domain"
)

func validRequest() domain.Request {
	return domain.Request{
		RequestID:      uuid.MustParse("0192f0a0-0000-7000-8000-000000000001"),
		ClinicID:       1,
		ComponentKind:  "RBC",
		RequestedUnits: 2,
		Urgency:        "URGENT",
		BloodGroup:     "A+",
		Phenotype:      "K-",
		Constraints:    []domain.Constraint{{Code: domain.ConstraintMinRemainingHours, Value: "24"}},
		CreatedAt:      time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC),
	}
}

func TestRequestValidate(t *testing.T) {
	if err := validRequest().Validate(); err != nil {
		t.Fatalf("корректная заявка отклонена: %v", err)
	}
	cases := map[string]func(r *domain.Request){
		"нет идентификатора":            func(r *domain.Request) { r.RequestID = uuid.Nil },
		"нет клиники":                   func(r *domain.Request) { r.ClinicID = 0 },
		"нет вида":                      func(r *domain.Request) { r.ComponentKind = "" },
		"ноль единиц":                   func(r *domain.Request) { r.RequestedUnits = 0 },
		"нет срочности":                 func(r *domain.Request) { r.Urgency = "" },
		"срочность строчными":           func(r *domain.Request) { r.Urgency = "urgent" },
		"опечатка в срочности":          func(r *domain.Request) { r.Urgency = "EMERGENSY" },
		"неизвестная группа":            func(r *domain.Request) { r.BloodGroup = "A" },
		"фенотип без знака":             func(r *domain.Request) { r.Phenotype = "K" },
		"фенотип противоречив":          func(r *domain.Request) { r.Phenotype = "K-K+" },
		"нет момента заявки":            func(r *domain.Request) { r.CreatedAt = time.Time{} },
		"неизвестное ограничение":       func(r *domain.Request) { r.Constraints = []domain.Constraint{{Code: "NO_SUCH"}} },
		"повтор ограничения":            func(r *domain.Request) { r.Constraints = append(r.Constraints, r.Constraints[0]) },
		"параметр часов не число":       func(r *domain.Request) { r.Constraints[0].Value = "сутки" },
		"параметр часов не положителен": func(r *domain.Request) { r.Constraints[0].Value = "0" },
		"лишний параметр": func(r *domain.Request) {
			r.Constraints = []domain.Constraint{{Code: domain.ConstraintNoGroupSubstitution, Value: "1"}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := validRequest()
			r.Constraints = append([]domain.Constraint(nil), r.Constraints...)
			mutate(&r)
			if err := r.Validate(); !errors.Is(err, domain.ErrInvalidRequest) {
				t.Fatalf("ожидали ErrInvalidRequest, получили %v", err)
			}
		})
	}
}

// Требования фенотипа «есть/нет» строгие: маркер, о котором у единицы нет сведений, требование не выполняет.
func TestPhenotypeSatisfies(t *testing.T) {
	cases := []struct {
		unit, req string
		want      bool
	}{
		{"C+c-E+e-K-", "", true},
		{"", "", true},
		{"C+c-E+e-K-", "K-", true},
		{"C+c-E+e-K-", "K+", false},
		{"C+c-E+e-K-", "C+ e-", true},
		{"C+c-E+e-K-", "c+", false},
		{"", "K-", false},
		{"C+c-", "K-", false},
		{"C+c-E+e-K-", "C+,c-,K-", true},
	}
	for _, tc := range cases {
		t.Run(tc.unit+"|"+tc.req, func(t *testing.T) {
			u, err := domain.ParsePhenotype(tc.unit)
			if err != nil {
				t.Fatal(err)
			}
			r, err := domain.ParsePhenotype(tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if got := u.Satisfies(r); got != tc.want {
				t.Fatalf("единица %q, требование %q: %v, ожидали %v", tc.unit, tc.req, got, tc.want)
			}
		})
	}
}

func TestParsePhenotypeRejectsGarbage(t *testing.T) {
	for _, s := range []string{"K", "+", "K+-", "K-K+", "К+"} {
		if _, err := domain.ParsePhenotype(s); err == nil {
			t.Fatalf("фенотип %q принят", s)
		}
	}
}
