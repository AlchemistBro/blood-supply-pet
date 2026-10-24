//go:build integration

package app_test

import (
	"context"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/fakeaist"
)

func TestSnapshotRepairsSameVersionAndWritesAudit(t *testing.T) {
	e := setup(t, 500)
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-1", StorageLocation: "correct"})
	e.sync(t)
	u := unitByCode(t, e.pool, "U-1")
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `UPDATE unit SET storage_location='corrupted' WHERE unit_id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	r, err := e.svc.SyncSnapshot(ctx)
	if err != nil || r.Applied != 1 || r.Result != "SUCCESS" {
		t.Fatalf("сверка: %+v %v", r, err)
	}
	var location string
	var version int64
	if err := e.pool.QueryRow(ctx, `SELECT storage_location,source_version FROM unit WHERE unit_id=$1`, u.ID).Scan(&location, &version); err != nil {
		t.Fatal(err)
	}
	if location != "correct" || version != u.Version {
		t.Fatalf("исправление: %s версия %d", location, version)
	}
	var audits int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_local WHERE action='SNAPSHOT_DISCREPANCY' AND object_id=$1`, u.ID.String()).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("расхождений в аудите %d", audits)
	}
}

func TestStockSnapshotClearsOldClinicAfterUnitMoves(t *testing.T) {
	e := setup(t, 500)
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-1", ClinicCode: "C001"})
	e.sync(t)
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-1", ClinicCode: "C002"})
	e.sync(t)
	zero := false
	for _, msg := range outbox(t, e.pool) {
		if msg.Type == "StockSnapshotPublished" && msg.Payload["clinic_id"] == float64(1) && msg.Payload["unit_count"] == float64(0) {
			zero = true
		}
	}
	if !zero {
		t.Fatal("снимок не обнулил запас прежней клиники")
	}
}
