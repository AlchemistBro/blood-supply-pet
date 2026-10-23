package testdata_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testdata"
)

// Демо-набор O15: десять версий правил, каждая помечена как синтетическая (ADR-0010),
// ссылки на справочники корректны, версии одного правила не перекрываются по сроку действия.
func TestDemoMatchingRulesAreMarkedAndConsistent(t *testing.T) {
	ref := refdata.MustLoad()
	rules := testdata.DemoMatchingRules()
	if len(rules) != 10 {
		t.Fatalf("демо-правил %d, ожидали 10", len(rules))
	}
	groups := map[string]bool{}
	for _, g := range ref.BloodGroups {
		groups[g.Code] = true
	}
	seen := map[string]bool{}
	byCode := map[string][]testdata.MatchingRule{}
	exceptions := 0
	for _, r := range rules {
		key := fmt.Sprintf("%s@%d", r.RuleCode, r.VersionNo)
		if seen[key] {
			t.Fatalf("версия %s повторяется", key)
		}
		seen[key] = true
		byCode[r.RuleCode] = append(byCode[r.RuleCode], r)
		if r.BasisDocument != testdata.DemoRulesBasisDocument || !strings.HasPrefix(r.BasisDocument, "ДЕМО") {
			t.Fatalf("%s: основание %q без пометки демо", r.RuleCode, r.BasisDocument)
		}
		if r.BasisClause == "" || r.VersionNo <= 0 || r.RulePriority <= 0 {
			t.Fatalf("%s: пустой пункт основания, версия или приоритет: %+v", r.RuleCode, r)
		}
		if _, ok := ref.Kind(r.ComponentKindCode); !ok {
			t.Fatalf("%s: неизвестный вид %q", r.RuleCode, r.ComponentKindCode)
		}
		if r.AllowedKindCode != "" {
			if _, ok := ref.Kind(r.AllowedKindCode); !ok {
				t.Fatalf("%s: неизвестный вид замены %q", r.RuleCode, r.AllowedKindCode)
			}
		}
		for _, g := range []string{r.RequestedGroupCode, r.AllowedGroupCode} {
			if g != "" && !groups[g] {
				t.Fatalf("%s: неизвестная группа %q", r.RuleCode, g)
			}
		}
		if r.ValidFrom.IsZero() || (!r.ValidTo.IsZero() && !r.ValidTo.After(r.ValidFrom)) {
			t.Fatalf("%s v%d: срок действия некорректен", r.RuleCode, r.VersionNo)
		}
		if r.IsException {
			exceptions++
		}
	}
	if exceptions == 0 {
		t.Fatal("в демо-наборе нет правила-исключения")
	}
	versioned := false
	for code, versions := range byCode {
		if len(versions) > 1 {
			versioned = true
		}
		for i := range versions {
			for j := i + 1; j < len(versions); j++ {
				a, b := versions[i], versions[j]
				aEndsBeforeB := !a.ValidTo.IsZero() && !a.ValidTo.After(b.ValidFrom)
				bEndsBeforeA := !b.ValidTo.IsZero() && !b.ValidTo.After(a.ValidFrom)
				if !aEndsBeforeB && !bEndsBeforeA {
					t.Fatalf("%s: версии %d и %d действуют одновременно", code, a.VersionNo, b.VersionNo)
				}
			}
		}
	}
	if !versioned {
		t.Fatal("в демо-наборе нет правила с несколькими версиями")
	}
}

// Набор возвращается копией: изменение результата не портит следующий вызов.
func TestDemoMatchingRulesReturnsCopy(t *testing.T) {
	a := testdata.DemoMatchingRules()
	a[0].RuleCode = "CHANGED"
	b := testdata.DemoMatchingRules()
	if b[0].RuleCode == "CHANGED" {
		t.Fatal("DemoMatchingRules отдаёт общий срез")
	}
}
