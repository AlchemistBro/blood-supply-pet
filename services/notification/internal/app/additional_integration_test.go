//go:build integration

package app_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/security"
)

func TestStockAndSecuritySignalsUseDepartmentMailboxes(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	stock := message(t, inventory.StockBelowNorm{ClinicID: 1, ComponentKind: "RBC", StockVolumeML: 100, NormML: 200, DetectedAt: start}, nil)
	incident := message(t, security.SecurityIncidentSuspected{IncidentID: uuid.New(), Rule: "PATIENT_CASE_READ_BURST", UserID: "doctor", Count: 21, WindowSeconds: 600, DetectedAt: start}, nil)
	for _, msg := range []struct{ security bool }{{false}, {true}} {
		m := stock
		if msg.security {
			m = incident
		}
		if err := e.svc.HandleMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.HandleMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM notification WHERE (kind='STOCK_BELOW_NORM' AND recipient_email='transfusion.c001@demo.blood-supply.local') OR (kind='SECURITY_INCIDENT' AND recipient_email='security@demo.blood-supply.local')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("неверные адресаты или дубли: %d", count)
	}
}
