package domain_test

import (
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
)

func TestNormUsesMinimumUnitAndThreeDecimalPlaces(t *testing.T) {
	for _, c := range []struct {
		annual, unit  int64
		monthly, norm float64
		minimum       bool
	}{
		{0, 300, 0, 300, true}, {360000, 300, 30000, 1000, false}, {108001, 300, 9000.083, 300.003, false},
	} {
		n, err := domain.CalculateNorm(c.annual, c.unit)
		if err != nil || n.MonthlyML != c.monthly || n.NormML != c.norm || n.Minimum != c.minimum {
			t.Fatalf("%+v: %+v %v", c, n, err)
		}
	}
	if _, err := domain.CalculateNorm(-1, 300); err == nil {
		t.Fatal("отрицательный расход принят")
	}
}
