package domain_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/domain"
)

// Фенотип заявки, единицы и спецификации замены в журнал не попадает (CONTRIBUTING.md, ADR-0011);
// содержимое ограничений заявки тоже.
func TestLogValueRedactsPhenotype(t *testing.T) {
	req := withPhenotype(request("RBC", "A+", "URGENT", 1,
		domain.Constraint{Code: domain.ConstraintMinRemainingHours, Value: "12"}), "Fya- K-")
	u := unit(1, remote2, "RBC", "A+", 5*day24)
	u.Phenotype = "C+c-E+e-K-Fya-"
	res := mustMatch(t, input(t, req, u))
	if len(res.Variants) != 1 {
		t.Fatalf("ожидали один вариант, получили %+v", views(res))
	}

	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			var h slog.Handler = slog.NewJSONHandler(&buf, nil)
			if format == "text" {
				h = slog.NewTextHandler(&buf, nil)
			}
			slog.New(h).Info("подбор",
				"request", req, "unit", u, "spec", res.Variants[0].Spec, "variant", res.Variants[0])
			out := buf.String()
			for _, leak := range []string{"Fya", "K-", "C+", "MIN_REMAINING_HOURS"} {
				if strings.Contains(out, leak) {
					t.Fatalf("в журнале %q:\n%s", leak, out)
				}
			}
			if strings.Count(out, "[скрыто]") < 4 {
				t.Fatalf("фенотип не заменён пометкой в каждом объекте:\n%s", out)
			}
			for _, want := range []string{req.RequestID.String(), u.UnitID.String(), "DEMO_RBC_IDENTICAL"} {
				if !strings.Contains(out, want) {
					t.Fatalf("в журнале нет %q:\n%s", want, out)
				}
			}
		})
	}
}
