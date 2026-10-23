//go:build integration

package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/fakeaist"
)

// Фактический запас (O02) считает только доступные по ref_unit_status и неистёкшие единицы своей клиники;
// граница годности — часы сервиса, а не время базы.
func TestActualStockCountsOnlyAvailableUnexpiredUnits(t *testing.T) {
	e := setup(t, 500)
	ctx := context.Background()
	e.aist.Add(fakeaist.Unit{AistUnitCode: "RBC-LONG", ComponentKind: "RBC", BloodGroup: "A+", VolumeML: 300, ExpiresAt: t0.Add(30 * 24 * time.Hour)})
	e.aist.Add(fakeaist.Unit{AistUnitCode: "RBC-SHORT", ComponentKind: "RBC", BloodGroup: "A+", VolumeML: 250, ExpiresAt: t0.Add(48 * time.Hour)})
	e.aist.Add(fakeaist.Unit{AistUnitCode: "RBC-QUAR", ComponentKind: "RBC", BloodGroup: "A+", Status: "QUARANTINE"})
	e.aist.Add(fakeaist.Unit{AistUnitCode: "RBC-EXPIRED", ComponentKind: "RBC", BloodGroup: "A+", ExpiresAt: t0.Add(-time.Hour)})
	e.aist.Add(fakeaist.Unit{AistUnitCode: "RBC-ISSUED", ComponentKind: "RBC", BloodGroup: "O-"})
	e.aist.Withdraw("RBC-ISSUED", "ISSUED")
	e.aist.Add(fakeaist.Unit{AistUnitCode: "FFP-1", ComponentKind: "FFP", BloodGroup: "A+", VolumeML: 220})
	e.aist.Add(fakeaist.Unit{AistUnitCode: "OTHER-CLINIC", ClinicCode: "C002", ComponentKind: "RBC", BloodGroup: "A+"})
	e.sync(t)

	s, err := e.svc.Stock(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Lines) != 2 {
		t.Fatalf("строк запаса %d, ожидали 2 (FFP A+, RBC A+): %+v", len(s.Lines), s.Lines)
	}
	ffp, rbc := s.Lines[0], s.Lines[1]
	if ffp.ComponentKind != "FFP" || ffp.UnitCount != 1 || ffp.VolumeML != 220 {
		t.Fatalf("FFP: %+v", ffp)
	}
	if rbc.ComponentKind != "RBC" || rbc.BloodGroup != "A+" || rbc.UnitCount != 2 || rbc.VolumeML != 550 ||
		!rbc.NearestExpiry.Equal(t0.Add(48*time.Hour)) {
		t.Fatalf("RBC A+: %+v", rbc)
	}

	// Через трое суток короткая единица истекла и выпала из запаса без всякого обмена с АИСТ-М.
	e.clock.Advance(72 * time.Hour)
	s, err = e.svc.Stock(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range s.Lines {
		if l.ComponentKind == "RBC" && (l.UnitCount != 1 || l.VolumeML != 300) {
			t.Fatalf("после истечения RBC A+: %+v", l)
		}
	}

	// Представление actual_stock — тот же расчёт: сверка с функцией на тот же момент.
	var viaFunc int64
	if err := e.pool.QueryRow(ctx, `SELECT coalesce(sum(unit_count), 0) FROM actual_stock_at($1) WHERE clinic_id = 1`, e.clock.Now()).
		Scan(&viaFunc); err != nil {
		t.Fatal(err)
	}
	if viaFunc != 2 {
		t.Fatalf("actual_stock_at: %d единиц, ожидали 2", viaFunc)
	}
	if _, err := e.pool.Exec(ctx, `SELECT * FROM actual_stock`); err != nil {
		t.Fatalf("представление actual_stock: %v", err)
	}

	if _, err := e.svc.Stock(ctx, 99999); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("неизвестная клиника: %v", err)
	}
}

// Водяной знак (NFR015) — окончание последнего успешного опроса; предупреждение — после порога (10 минут, демо).
func TestStockWatermarkAndStaleness(t *testing.T) {
	e := setup(t, 500)
	ctx := context.Background()
	s, err := e.svc.Stock(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if s.Watermark != nil || !s.Stale {
		t.Fatalf("до первого опроса: водяной знак %v, устарело %v", s.Watermark, s.Stale)
	}

	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-1"})
	e.sync(t)
	syncedAt := e.clock.Now()
	s, err = e.svc.Stock(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if s.Watermark == nil || !s.Watermark.Equal(syncedAt) || s.Stale {
		t.Fatalf("после опроса: %v, устарело %v", s.Watermark, s.Stale)
	}

	e.aist.SetDown(true)
	e.clock.Advance(11 * time.Minute)
	if _, err := e.svc.SyncOnce(ctx); err == nil {
		t.Fatal("опрос при недоступной АИСТ-М должен завершиться ошибкой")
	}
	s, err = e.svc.Stock(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Watermark.Equal(syncedAt) || !s.Stale {
		t.Fatalf("после 11 минут без успешного опроса: %v, устарело %v", s.Watermark, s.Stale)
	}
}

// FEFO (FR015): порядок списка совпадает с правилом домена; фильтры по виду и группе; страницы.
func TestUnitsFEFOFiltersAndPages(t *testing.T) {
	e := setup(t, 500)
	ctx := context.Background()
	hours := []int{100, 20, 60, 20, 5}
	for i, h := range hours {
		e.aist.Add(fakeaist.Unit{AistUnitCode: "R-" + string(rune('A'+i)), ComponentKind: "RBC", BloodGroup: "O-", ExpiresAt: t0.Add(time.Duration(h) * time.Hour)})
	}
	e.aist.Add(fakeaist.Unit{AistUnitCode: "P-1", ComponentKind: "PLT", BloodGroup: "O-", ExpiresAt: t0.Add(time.Hour)})
	e.aist.Add(fakeaist.Unit{AistUnitCode: "R-AB", ComponentKind: "RBC", BloodGroup: "AB+", ExpiresAt: t0.Add(2 * time.Hour)})
	e.aist.Add(fakeaist.Unit{AistUnitCode: "R-Q", ComponentKind: "RBC", BloodGroup: "O-", Status: "QUARANTINE", ExpiresAt: t0.Add(3 * time.Hour)})
	e.sync(t)

	page, err := e.svc.Units(ctx, app.UnitFilter{ClinicID: 1, ComponentKind: "RBC", BloodGroup: "O-", FEFO: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Units) != 5 || page.HasMore {
		t.Fatalf("RBC O-: %d единиц, has_more %v", len(page.Units), page.HasMore)
	}
	sorted := make([]domain.Unit, 0, len(page.Units))
	for _, u := range page.Units {
		sorted = append(sorted, u.Unit)
		if !u.Available || u.Status != domain.StatusInStock || u.RhPositive || u.AistUnitCode == "" {
			t.Fatalf("единица в списке: %+v", u)
		}
	}
	domain.SortFEFO(sorted)
	for i := range sorted {
		if sorted[i].UnitID != page.Units[i].UnitID {
			t.Fatalf("порядок SQL расходится с FEFO домена на позиции %d", i)
		}
	}
	if !page.Units[0].ExpiresAt.Equal(t0.Add(5 * time.Hour)) {
		t.Fatalf("первой должна идти единица с ближайшей годностью: %v", page.Units[0].ExpiresAt)
	}

	first, err := e.svc.Units(ctx, app.UnitFilter{ClinicID: 1, ComponentKind: "RBC", BloodGroup: "O-", FEFO: true, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.svc.Units(ctx, app.UnitFilter{ClinicID: 1, ComponentKind: "RBC", BloodGroup: "O-", FEFO: true, Limit: 2, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !first.HasMore || len(first.Units) != 2 || second.Units[0].UnitID != page.Units[2].UnitID {
		t.Fatalf("страницы: первая %d (has_more %v), вторая начинается не с третьей единицы", len(first.Units), first.HasMore)
	}

	all, err := e.svc.Units(ctx, app.UnitFilter{ClinicID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Units) != 7 {
		t.Fatalf("без фильтров: %d единиц, ожидали 7 (карантин не входит)", len(all.Units))
	}

	for _, bad := range []app.UnitFilter{
		{ClinicID: 1, ComponentKind: "BLOOD"},
		{ClinicID: 1, BloodGroup: "Z+"},
		{ClinicID: 1, Limit: 1001},
		{ClinicID: 1, Offset: -1},
	} {
		if _, err := e.svc.Units(ctx, bad); !errors.Is(err, app.ErrInvalid) {
			t.Errorf("%+v: ожидали ErrInvalid, получили %v", bad, err)
		}
	}
	if _, err := e.svc.Units(ctx, app.UnitFilter{ClinicID: 99999}); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("неизвестная клиника: %v", err)
	}
}

// Карточка единицы в любом статусе и история её состояний по версиям (FR008).
func TestUnitCardAndHistory(t *testing.T) {
	e := setup(t, 500)
	ctx := context.Background()
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-1", BloodGroup: "B-", Phenotype: "CcDEe"})
	e.sync(t)
	e.aist.SetStatus("U-1", "QUARANTINE")
	e.sync(t)
	e.aist.SetStatus("U-1", "IN_STOCK")
	e.sync(t)
	e.aist.Withdraw("U-1", "ISSUED")
	e.sync(t)

	id := unitByCode(t, e.pool, "U-1").ID
	u, history, err := e.svc.UnitHistory(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if u.Status != domain.StatusIssued || u.Available || u.RhPositive || u.Phenotype != "CcDEe" || u.ClinicID != 1 {
		t.Fatalf("карточка: %+v", u)
	}
	want := []string{"IN_STOCK", "QUARANTINE", "IN_STOCK", "ISSUED"}
	if len(history) != len(want) {
		t.Fatalf("история: %+v", history)
	}
	for i, h := range history {
		if h.Status != want[i] || h.SourceVersion != int64(i+1) {
			t.Fatalf("история[%d]: %+v", i, h)
		}
	}
	if _, err := e.svc.Unit(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("неизвестная единица: %v", err)
	}
}

// Последний сеанс и последний успешный; журнал сеансов новыми первыми с курсором.
func TestSyncOverviewAndRuns(t *testing.T) {
	e := setup(t, 500)
	ctx := context.Background()
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-1"})
	e.sync(t)
	second := e.sync(t)
	e.aist.SetDown(true)
	e.clock.Advance(5 * time.Second)
	failed, _ := e.svc.SyncOnce(ctx)

	o, err := e.svc.LastSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if o.LastRun == nil || o.LastRun.ID != failed.RunID || *o.LastRun.Result != app.SyncFailed || o.LastRun.Note == nil {
		t.Fatalf("последний сеанс: %+v", o.LastRun)
	}
	if o.LastSuccess == nil || o.LastSuccess.ID != second.RunID {
		t.Fatalf("последний успешный: %+v", o.LastSuccess)
	}

	p1, err := e.svc.SyncRuns(ctx, app.SyncRunFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Runs) != 2 || p1.Runs[0].ID != failed.RunID || p1.Runs[1].ID != second.RunID || p1.NextBefore == nil {
		t.Fatalf("первая страница: %+v", p1)
	}
	p2, err := e.svc.SyncRuns(ctx, app.SyncRunFilter{Limit: 2, BeforeID: p1.NextBefore})
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Runs) != 1 || p2.NextBefore != nil {
		t.Fatalf("вторая страница: %+v", p2)
	}
	if _, err := e.svc.SyncRuns(ctx, app.SyncRunFilter{Kind: "WEEKLY"}); !errors.Is(err, app.ErrInvalid) {
		t.Fatalf("неизвестный вид сеанса: %v", err)
	}
}
