package testdata_test

import (
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testdata"
)

func TestGenerateIsDeterministicAndPlausible(t *testing.T) {
	ref := refdata.MustLoad()
	cfg := testdata.DefaultConfig()
	cfg.Units = 5000
	a := testdata.Generate(ref, cfg)
	b := testdata.Generate(ref, cfg)
	if len(a.Units) != 5000 || len(a.Units) != len(b.Units) {
		t.Fatalf("размер набора %d/%d", len(a.Units), len(b.Units))
	}
	for i := range a.Units {
		if a.Units[i] != b.Units[i] {
			t.Fatalf("набор недетерминирован на позиции %d", i)
		}
	}
	inStock, codes := 0, map[string]bool{}
	for _, u := range a.Units {
		if codes[u.AistUnitCode] {
			t.Fatalf("код единицы повторяется: %s", u.AistUnitCode)
		}
		codes[u.AistUnitCode] = true
		if u.Status == "IN_STOCK" {
			inStock++
		}
		if u.VolumeML <= 0 || u.ExpiresAt.Before(cfg.Now) {
			t.Fatalf("неправдоподобная единица: %+v", u)
		}
	}
	if share := float64(inStock) / float64(len(a.Units)); share < 0.9 || share > 0.99 {
		t.Fatalf("доля в наличии %.3f вне ожидаемого диапазона", share)
	}
	if len(a.Consumption) == 0 {
		t.Fatal("годовой расход не сгенерирован")
	}
}
