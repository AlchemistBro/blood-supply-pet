package phenotype_test

import (
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/phenotype"
)

func TestRequirementsCannotBeWeakenedByAdditionalMarkers(t *testing.T) {
	for _, tc := range []struct {
		actual, required string
		want             bool
	}{
		{"C+c-E+e-K-", "K-", true}, {"K+", "K-", false}, {"C+", "K-", false}, {"K+K-", "K-", false},
		{"CCDee", "CCDee", true}, {"ccdee", "CCDee", false}, {"K- C+", "C+K-", true},
		{"K+K-", "K+K-", false},
	} {
		if got := phenotype.Matches(tc.actual, tc.required); got != tc.want {
			t.Fatalf("результат проверки требований: %v, ожидали %v", got, tc.want)
		}
	}
}
