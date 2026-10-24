//go:build integration

package app_test

import (
	"context"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/app"
)

func TestDemoNormSeedPreservesManualValueAndIsIdempotent(t *testing.T) {
	e := setup(t, 500)
	ctx := context.Background()
	actor := app.Actor{UserID: "it1", Role: "IT_SERVICE"}
	manual, err := e.svc.SetAnnualConsumption(ctx, actor, 1, "RBC", 2025, 999999)
	if err != nil {
		t.Fatal(err)
	}
	created, err := e.svc.SeedDemoNorms(ctx, actor, 2025)
	if err != nil || created == 0 {
		t.Fatalf("наполнение: %d %v", created, err)
	}
	repeated, err := e.svc.SeedDemoNorms(ctx, actor, 2025)
	if err != nil || repeated != 0 {
		t.Fatalf("повтор: %d %v", repeated, err)
	}
	norms, err := e.svc.Norms(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range norms {
		if n.Kind == "RBC" {
			found = true
			if n.ID != manual.ID || n.Annual != manual.Annual || n.Version != manual.Version {
				t.Fatal("ручной норматив изменён")
			}
		}
	}
	if !found {
		t.Fatal("ручной норматив потерян")
	}
}
