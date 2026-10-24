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

func countSnapshots(t *testing.T, e env) int {
	t.Helper()
	n := 0
	for _, msg := range outbox(t, e.pool) {
		if msg.Type == "StockSnapshotPublished" {
			n++
		}
	}
	return n
}

// Холостой опрос (ничего не изменилось) не должен каждые пять секунд публиковать полный снимок запаса:
// иначе outbox и проекции подписчиков растут без предела. Водяной знак NFR015 при этом обязан двигаться.
func TestIdlePollDoesNotRepublishStockSnapshotsButMovesWatermark(t *testing.T) {
	e := setup(t, 500)
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-1", ClinicCode: "C001"})
	e.sync(t)
	first := countSnapshots(t, e)
	if first == 0 {
		t.Fatal("опрос с изменением обязан опубликовать снимок")
	}
	var runsBefore int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM sync_run WHERE sync_result='SUCCESS'`).Scan(&runsBefore); err != nil {
		t.Fatal(err)
	}
	e.sync(t)
	e.sync(t)
	if got := countSnapshots(t, e); got != first {
		t.Fatalf("холостые опросы опубликовали снимки: было %d, стало %d", first, got)
	}
	var runsAfter int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM sync_run WHERE sync_result='SUCCESS'`).Scan(&runsAfter); err != nil {
		t.Fatal(err)
	}
	if runsAfter != runsBefore+2 {
		t.Fatalf("успешные прогоны должны фиксироваться для водяного знака: было %d, стало %d", runsBefore, runsAfter)
	}
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-2", ClinicCode: "C001"})
	e.sync(t)
	if got := countSnapshots(t, e); got <= first {
		t.Fatalf("после изменения снимок обязан обновиться: было %d, стало %d", first, got)
	}
}
