//go:build integration

package app_test

import (
	"context"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/app"
)

func TestNormAndBelowThresholdAreTransactionalAndNotRepeated(t *testing.T) {
	e := setup(t, 500)
	ctx := context.Background()
	n, err := e.svc.SetAnnualConsumption(ctx, app.Actor{UserID: "operator-c001", Role: "CLINIC_OPERATOR"}, 1, "RBC", 2026, 360000)
	if err != nil || n.NormML != 1000 {
		t.Fatalf("норматив: %+v %v", n, err)
	}
	e.sync(t)
	e.sync(t)
	counts := map[string]int{}
	for _, msg := range outbox(t, e.pool) {
		counts[msg.Type]++
	}
	if counts["StockNormPublished"] != 1 || counts["StockBelowNorm"] != 1 {
		t.Fatalf("события норматива: %v", counts)
	}
	items, err := e.svc.Norms(ctx, 1)
	if err != nil || len(items) != 1 || items[0].Version != n.Version {
		t.Fatalf("действующий норматив: %+v %v", items, err)
	}
}

func TestOnDemandRecordsRequestingClinic(t *testing.T) {
	e := setup(t, 500)
	r, err := e.svc.SyncOnDemand(context.Background(), 1, app.Actor{UserID: "operator-c001"})
	if err != nil || r.Result != app.SyncSuccess {
		t.Fatalf("обновление: %+v %v", r, err)
	}
	var clinic int64
	var kind, actor string
	if err := e.pool.QueryRow(context.Background(), `SELECT clinic_id,sync_kind,requested_by FROM sync_run WHERE sync_run_id=$1`, r.RunID).Scan(&clinic, &kind, &actor); err != nil {
		t.Fatal(err)
	}
	if clinic != 1 || kind != "ON_DEMAND" || actor != "operator-c001" {
		t.Fatalf("сеанс: %d %s %s", clinic, kind, actor)
	}
}
