//go:build integration

package app_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/fakeaist"
)

// Ни одной единицы нет в копии: спрашивать АИСТ-М не о чем, но это не её недоступность —
// ответ source=aist, все id в not_found (иначе transfer ошибочно эскалирует «источник недоступен»).
func TestUnitsUnknownToCopyAreNotFoundNotCopy(t *testing.T) {
	e := setup(t, 500)
	a, b := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	rep, err := e.svc.UnitStatuses(context.Background(), []uuid.UUID{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Source != app.SourceAist || len(rep.Units) != 0 || len(rep.NotFound) != 2 {
		t.Fatalf("ответ для неизвестных единиц: %+v", rep)
	}
	if e.aist.StatusCalls() != 0 {
		t.Fatal("АИСТ-М спрашивали без единого кода")
	}
}

// Единица, о которой АИСТ-М промолчала (нет ни в units, ни в not_found), считается ненайденной.
func TestUnitSilentInSimulatorIsNotFound(t *testing.T) {
	e := setup(t, 500)
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-1"})
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-2"})
	e.sync(t)
	u1, u2 := unitByCode(t, e.pool, "U-1"), unitByCode(t, e.pool, "U-2")
	e.aist.SetSilent("U-2")
	rep, err := e.svc.UnitStatuses(context.Background(), []uuid.UUID{u1.ID, u2.ID})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Source != app.SourceAist || len(rep.Units) != 1 || rep.Units[0].UnitID != u1.ID ||
		len(rep.NotFound) != 1 || rep.NotFound[0] != u2.ID {
		t.Fatalf("ответ: %+v", rep)
	}
}

// Медленная АИСТ-М не съедает бюджет transfer → inventory (ADR-0004): вызов прерывается за резерв
// до дедлайна входящего запроса, и ответ из копии приходит вовремя.
func TestSlowSimulatorFallsBackToCopyWithinDeadline(t *testing.T) {
	e := setupWith(t, app.Config{StatusTimeout: 2 * time.Second, StatusReserve: 300 * time.Millisecond},
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-1"})
	e.sync(t)
	u := unitByCode(t, e.pool, "U-1")
	e.aist.SetSlow(10 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	rep, err := e.svc.UnitStatuses(ctx, []uuid.UUID{u.ID})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Source != app.SourceCopy || len(rep.Units) != 1 {
		t.Fatalf("ответ при медленной АИСТ-М: %+v", rep)
	}
	if elapsed >= time.Second-100*time.Millisecond {
		t.Fatalf("ответ из копии через %v: резерв до дедлайна 1 с не соблюдён", elapsed)
	}
}

// Без дедлайна у входящего запроса вызов АИСТ-М ограничен BS_AIST_STATUS_TIMEOUT.
func TestSlowSimulatorIsCutByStatusTimeout(t *testing.T) {
	e := setupWith(t, app.Config{StatusTimeout: 200 * time.Millisecond}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-1"})
	e.sync(t)
	u := unitByCode(t, e.pool, "U-1")
	e.aist.SetSlow(10 * time.Second)
	started := time.Now()
	rep, err := e.svc.UnitStatuses(context.Background(), []uuid.UUID{u.ID})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Source != app.SourceCopy || time.Since(started) > 2*time.Second {
		t.Fatalf("ответ %s через %v", rep.Source, time.Since(started))
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Отказ АИСТ-М логируется видом отказа без адреса запроса: исходные коды единиц в лог не попадают (ADR-0010).
func TestAistFailuresDoNotLeakUnitCodesToLogs(t *testing.T) {
	logs := &syncBuffer{}
	e := setupWith(t, app.Config{StatusTimeout: 100 * time.Millisecond}, slog.New(slog.NewJSONHandler(logs, nil)))
	e.aist.Add(fakeaist.Unit{AistUnitCode: "SECRET-CODE-1"})
	e.aist.Add(fakeaist.Unit{AistUnitCode: "SECRET-CODE-2", ClinicCode: "C999"}) // отброшенная запись
	e.sync(t)
	u := unitByCode(t, e.pool, "SECRET-CODE-1")

	e.aist.SetSlow(5 * time.Second) // таймаут: *url.Error содержал бы адрес с ids=SECRET-CODE-1
	if _, err := e.svc.UnitStatuses(context.Background(), []uuid.UUID{u.ID}); err != nil {
		t.Fatal(err)
	}
	e.aist.SetSlow(0)
	e.aist.SetDown(true)
	if _, err := e.svc.UnitStatuses(context.Background(), []uuid.UUID{u.ID}); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(5 * time.Second)
	res, _ := e.svc.SyncOnce(context.Background())

	out := logs.String()
	if !strings.Contains(out, "failure") {
		t.Fatalf("в логе нет вида отказа: %s", out)
	}
	if strings.Contains(out, "SECRET-CODE") {
		t.Fatalf("исходный код единицы АИСТ-М попал в лог: %s", out)
	}
	var note *string
	if err := e.pool.QueryRow(context.Background(), `SELECT error_text FROM sync_run WHERE sync_run_id = $1`, res.RunID).Scan(&note); err != nil {
		t.Fatal(err)
	}
	if note == nil || strings.Contains(*note, "SECRET-CODE") || strings.Contains(*note, "http://") {
		t.Fatalf("error_text сеанса: %v", note)
	}
}

// Два одновременных опроса не мешают друг другу: advisory-блокировка пропускает второй, событий не больше,
// чем изменений.
func TestParallelSyncOnceIsSerialized(t *testing.T) {
	e := setup(t, 500)
	for _, code := range []string{"U-1", "U-2", "U-3"} {
		e.aist.Add(fakeaist.Unit{AistUnitCode: code})
	}
	e.aist.SetSlow(300 * time.Millisecond)
	var wg sync.WaitGroup
	results := make([]app.SyncResult, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = e.svc.SyncOnce(context.Background())
		}()
	}
	wg.Wait()
	skipped := 0
	for i := range 2 {
		if errs[i] != nil {
			t.Fatalf("опрос %d: %v", i, errs[i])
		}
		if results[i].Skipped {
			skipped++
		}
	}
	if skipped != 1 {
		t.Fatalf("пропущено опросов %d, ожидали 1: %+v", skipped, results)
	}
	if n := len(outbox(t, e.pool)); n != 3 {
		t.Fatalf("событий %d, ожидали 3", n)
	}
}
