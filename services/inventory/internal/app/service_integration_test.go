//go:build integration

package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testinfra"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/adapters/aistclient"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/adapters/events"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/fakeaist"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/migrations"
)

var t0 = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)

type env struct {
	svc   *app.Service
	pool  *pgxpool.Pool
	aist  *fakeaist.Server
	clock *clock.Mutable
}

func setup(t *testing.T, pageLimit int) env {
	t.Helper()
	ctx := context.Background()
	pool, err := bspgx.Connect(ctx, testinfra.Postgres(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := bspgx.Migrate(ctx, pool, messaging.Migrations, refdata.Migrations, migrations.FS); err != nil {
		t.Fatal(err)
	}
	ref := refdata.MustLoad()
	if err := bspgx.WithTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error { return ref.Seed(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewMutable(t0)
	aist := fakeaist.New(t, t0)
	journal := messaging.NewJournal("inventory", auditc.ModuleInventory, clk)
	svc := app.New(app.Deps{
		Pool: pool, Store: postgres.New(pool), Aist: aistclient.New(aist.URL, aistclient.Options{StatusTimeout: 3 * time.Second}),
		Publisher: events.New(journal), Journal: journal, Ref: ref, Clock: clk,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: app.Config{PageLimit: pageLimit, StatusTimeout: 3 * time.Second},
	})
	return env{svc: svc, pool: pool, aist: aist, clock: clk}
}

func (e env) sync(t *testing.T) app.SyncResult {
	t.Helper()
	e.clock.Advance(5 * time.Second)
	res, err := e.svc.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("опрос: %v", err)
	}
	return res
}

type outboxRow struct {
	Type    string
	Key     string
	Payload map[string]any
	Raw     string
}

func outbox(t *testing.T, pool *pgxpool.Pool) []outboxRow {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT message_type, key, payload::text FROM outbox WHERE topic = 'inventory.events' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var r outboxRow
		if err := rows.Scan(&r.Type, &r.Key, &r.Raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(r.Raw), &r.Payload); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

type unitRow struct {
	ID      uuid.UUID
	Status  string
	Version int64
	Updated time.Time
}

func unitByCode(t *testing.T, pool *pgxpool.Pool, code string) unitRow {
	t.Helper()
	var u unitRow
	err := pool.QueryRow(context.Background(),
		`SELECT unit_id, unit_status_code, source_version, status_received_at FROM unit WHERE aist_unit_code = $1`, code).
		Scan(&u.ID, &u.Status, &u.Version, &u.Updated)
	if err != nil {
		t.Fatalf("единица %s: %v", code, err)
	}
	return u
}

func historyCount(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM unit_status_history WHERE unit_id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Новая единица регистрируется один раз; повторный опрос тех же версий ничего не меняет.
func TestNewUnitIsRegisteredOnceAndRepeatChangesNothing(t *testing.T) {
	e := setup(t, 500)
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-1", ClinicCode: "C002", BloodGroup: "O-"})
	res := e.sync(t)
	if res.Applied != 1 || res.Result != app.SyncSuccess {
		t.Fatalf("первый опрос: %+v", res)
	}
	u := unitByCode(t, e.pool, "U-1")
	msgs := outbox(t, e.pool)
	if len(msgs) != 1 || msgs[0].Type != "UnitRegistered" || msgs[0].Key != u.ID.String() {
		t.Fatalf("outbox после регистрации: %+v", msgs)
	}
	p := msgs[0].Payload
	if p["status"] != "IN_STOCK" || p["clinic_id"] != float64(2) || p["rh_positive"] != false || p["version"] != float64(1) {
		t.Fatalf("UnitRegistered: %v", p)
	}
	if strings.Contains(msgs[0].Raw, "U-1") {
		t.Fatalf("код единицы АИСТ-М попал в событие (ADR-0010): %s", msgs[0].Raw)
	}

	// Курсор не даёт повторить, поэтому повтор делается явно: сеанс с курсором 0 перечитывает всё.
	forceCursor(t, e, 0)
	e.sync(t)
	if got := outbox(t, e.pool); len(got) != 1 {
		t.Fatalf("повтор той же версии дал новые события: %+v", got)
	}
	again := unitByCode(t, e.pool, "U-1")
	if again != u {
		t.Fatalf("повтор изменил единицу: было %+v, стало %+v", u, again)
	}
	if n := historyCount(t, e.pool, u.ID); n != 1 {
		t.Fatalf("строк истории %d, ожидали 1", n)
	}
}

// forceCursor записывает завершённый сеанс с заданным курсором, как будто опрос начинается заново.
func forceCursor(t *testing.T, e env, cursor int64) {
	t.Helper()
	e.clock.Advance(time.Second)
	_, err := e.pool.Exec(context.Background(), `
		INSERT INTO sync_run (sync_run_id, sync_kind, requested_by, started_at, finished_at, sync_result, source_version)
		VALUES ($1, 'CHANGES', 'test', $2, $2, 'SUCCESS', $3)`, uuid.Must(uuid.NewV7()), e.clock.Now(), cursor)
	if err != nil {
		t.Fatal(err)
	}
}

// Устаревшая версия из источника не перезаписывает новую: это правило source_version.
// Сценарий — перезапуск имитатора: та же единица приходит заново с версией меньше сохранённой.
func TestStaleVersionDoesNotOverwriteNewer(t *testing.T) {
	e := setup(t, 500)
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-1"})
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-2"})
	e.aist.Withdraw("U-1", "ISSUED") // версия 3
	e.sync(t)
	before := unitByCode(t, e.pool, "U-1")
	if before.Status != domain.StatusIssued || before.Version != 3 {
		t.Fatalf("до перезапуска: %+v", before)
	}
	events := len(outbox(t, e.pool))

	e.aist.Restart()
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-1"}) // снова в наличии, версия 1 < 3
	res := e.sync(t)
	if !res.Resynced {
		t.Fatalf("перезапуск источника не замечен: %+v", res)
	}
	after := unitByCode(t, e.pool, "U-1")
	if after != before {
		t.Fatalf("устаревшая версия перезаписала единицу: было %+v, стало %+v", before, after)
	}
	if got := len(outbox(t, e.pool)); got != events {
		t.Fatalf("устаревшая версия дала события: было %d, стало %d", events, got)
	}
}

// Перезапуск АИСТ-М (current_version меньше курсора) запускает пересинхронизацию с 0:
// новые единицы приходят, сеанс помечен в sync_run, курсор — по новому источнику.
func TestSimulatorRestartTriggersResyncFromZero(t *testing.T) {
	e := setup(t, 500)
	for _, code := range []string{"U-1", "U-2", "U-3"} {
		e.aist.Add(fakeaist.Unit{AistUnitCode: code})
	}
	e.aist.Withdraw("U-1", "DISCARDED")
	if res := e.sync(t); res.To != 4 {
		t.Fatalf("курсор до перезапуска %d, ожидали 4", res.To)
	}

	e.aist.Restart()
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-9"}) // версия 1
	res := e.sync(t)
	if !res.Resynced || res.Applied != 1 || res.To != 1 || res.Result != app.SyncSuccess {
		t.Fatalf("пересинхронизация: %+v", res)
	}
	unitByCode(t, e.pool, "U-9")
	var note *string
	var cursor int64
	if err := e.pool.QueryRow(context.Background(),
		`SELECT error_text, source_version FROM sync_run WHERE sync_run_id = $1`, res.RunID).Scan(&note, &cursor); err != nil {
		t.Fatal(err)
	}
	if note == nil || !strings.Contains(*note, "пересинхронизация с 0") || cursor != 1 {
		t.Fatalf("sync_run после перезапуска: note=%v cursor=%d", note, cursor)
	}
	// Следующий опрос продолжает от нового курсора без повторной пересинхронизации.
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-10"})
	if res := e.sync(t); res.Resynced || res.Applied != 1 || res.To != 2 {
		t.Fatalf("опрос после пересинхронизации: %+v", res)
	}
}

// Выдача в АИСТ-М превращается в UnitWithdrawn с причиной ISSUED и ключом unit_id.
func TestIssueInSimulatorBecomesUnitWithdrawn(t *testing.T) {
	e := setup(t, 500)
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-1"})
	e.sync(t)
	e.aist.Withdraw("U-1", "ISSUED")
	e.sync(t)

	u := unitByCode(t, e.pool, "U-1")
	if u.Status != domain.StatusIssued || u.Version != 2 {
		t.Fatalf("единица после выдачи: %+v", u)
	}
	msgs := outbox(t, e.pool)
	last := msgs[len(msgs)-1]
	if last.Type != "UnitWithdrawn" || last.Key != u.ID.String() || last.Payload["reason"] != "ISSUED" || last.Payload["version"] != float64(2) {
		t.Fatalf("событие выдачи: %+v", last)
	}
	if n := historyCount(t, e.pool, u.ID); n != 2 {
		t.Fatalf("строк истории %d, ожидали 2", n)
	}
}

// Истёкший срок приходит как DISCARDED с withdrawal_reason=EXPIRED: причина события — EXPIRED.
func TestExpiryBecomesUnitWithdrawnExpired(t *testing.T) {
	e := setup(t, 500)
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-1"})
	e.sync(t)
	e.aist.Withdraw("U-1", "EXPIRED")
	e.sync(t)
	if u := unitByCode(t, e.pool, "U-1"); u.Status != domain.StatusDiscarded {
		t.Fatalf("статус %s, ожидали DISCARDED", u.Status)
	}
	msgs := outbox(t, e.pool)
	if last := msgs[len(msgs)-1]; last.Type != "UnitWithdrawn" || last.Payload["reason"] != "EXPIRED" {
		t.Fatalf("событие: %+v", last)
	}
}

// Карантин — UnitUpdated со статусом QUARANTINE, и единица недоступна в точечном статусе (ПП РФ № 641, п. 62).
func TestQuarantineIsUpdatedAndUnavailable(t *testing.T) {
	e := setup(t, 500)
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-1"})
	e.sync(t)
	e.aist.SetStatus("U-1", "QUARANTINE")
	e.sync(t)

	u := unitByCode(t, e.pool, "U-1")
	msgs := outbox(t, e.pool)
	last := msgs[len(msgs)-1]
	if last.Type != "UnitUpdated" || last.Payload["status"] != "QUARANTINE" || last.Key != u.ID.String() {
		t.Fatalf("событие карантина: %+v", last)
	}
	rep, err := e.svc.UnitStatuses(context.Background(), []uuid.UUID{u.ID})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Source != app.SourceAist || len(rep.Units) != 1 || rep.Units[0].Available || rep.Units[0].Status != "QUARANTINE" {
		t.Fatalf("статус единицы на карантине: %+v", rep)
	}

	e.aist.SetStatus("U-1", "IN_STOCK")
	rep, err = e.svc.UnitStatuses(context.Background(), []uuid.UUID{u.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Units[0].Available || rep.Units[0].Status != "IN_STOCK" {
		t.Fatalf("после снятия карантина статус из АИСТ-М: %+v", rep.Units[0])
	}
}

// АИСТ-М недоступна: статус приходит из копии с source=copy и водяным знаком последнего успешного опроса.
func TestStatusComesFromCopyWhenSimulatorIsDown(t *testing.T) {
	e := setup(t, 500)
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-1"})
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-2", Status: "QUARANTINE"})
	e.sync(t)
	syncedAt := e.clock.Now()
	u1, u2 := unitByCode(t, e.pool, "U-1"), unitByCode(t, e.pool, "U-2")
	unknown := uuid.Must(uuid.NewV7())

	e.aist.SetDown(true)
	e.clock.Advance(time.Minute)
	rep, err := e.svc.UnitStatuses(context.Background(), []uuid.UUID{u1.ID, u2.ID, unknown})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Source != app.SourceCopy {
		t.Fatalf("источник %q, ожидали copy", rep.Source)
	}
	if rep.Watermark == nil || !rep.Watermark.Equal(syncedAt) {
		t.Fatalf("водяной знак %v, ожидали %v", rep.Watermark, syncedAt)
	}
	got := map[uuid.UUID]app.UnitStatus{}
	for _, s := range rep.Units {
		got[s.UnitID] = s
	}
	if !got[u1.ID].Available || got[u2.ID].Available || got[u2.ID].Status != "QUARANTINE" {
		t.Fatalf("статусы из копии: %+v", rep.Units)
	}
	if len(rep.NotFound) != 1 || rep.NotFound[0] != unknown {
		t.Fatalf("not_found: %v", rep.NotFound)
	}
	if e.aist.StatusCalls() == 0 {
		t.Fatal("АИСТ-М не спрашивали: ответ из копии допустим только после отказа источника")
	}
}

// Неудачный опрос записан в sync_run как FAILED, водяной знак не двигается.
func TestFailedSyncIsRecordedAndWatermarkStays(t *testing.T) {
	e := setup(t, 500)
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-1"})
	ok := e.sync(t)
	e.aist.SetDown(true)
	e.clock.Advance(5 * time.Second)
	res, err := e.svc.SyncOnce(context.Background())
	if err == nil || res.Result != app.SyncFailed {
		t.Fatalf("опрос при недоступной АИСТ-М: %+v, %v", res, err)
	}
	var result string
	var errText *string
	if err := e.pool.QueryRow(context.Background(), `SELECT sync_result, error_text FROM sync_run WHERE sync_run_id = $1`, res.RunID).
		Scan(&result, &errText); err != nil {
		t.Fatal(err)
	}
	if result != app.SyncFailed || errText == nil || *errText == "" {
		t.Fatalf("sync_run: %s %v", result, errText)
	}
	var watermark time.Time
	if err := e.pool.QueryRow(context.Background(), `SELECT finished_at FROM sync_run WHERE sync_run_id = $1`, ok.RunID).Scan(&watermark); err != nil {
		t.Fatal(err)
	}
	u := unitByCode(t, e.pool, "U-1")
	rep, err := e.svc.UnitStatuses(context.Background(), []uuid.UUID{u.ID})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Watermark == nil || !rep.Watermark.Equal(watermark) {
		t.Fatalf("водяной знак сдвинулся неудачным опросом: %v, ожидали %v", rep.Watermark, watermark)
	}
}

// Страницы обходятся за один сеанс, курсор и счётчик изменений — в sync_run.
func TestPagesAdvanceCursor(t *testing.T) {
	e := setup(t, 2)
	for i := range 5 {
		e.aist.Add(fakeaist.Unit{AistUnitCode: "U-" + string(rune('A'+i))})
	}
	res := e.sync(t)
	if res.Applied != 5 || res.To != 5 || res.Result != app.SyncSuccess {
		t.Fatalf("опрос по страницам: %+v", res)
	}
	var cursor int64
	var count int
	if err := e.pool.QueryRow(context.Background(), `SELECT source_version, changes_count FROM sync_run WHERE sync_run_id = $1`, res.RunID).
		Scan(&cursor, &count); err != nil {
		t.Fatal(err)
	}
	if cursor != 5 || count != 5 {
		t.Fatalf("sync_run: курсор %d, изменений %d", cursor, count)
	}
}

// Некорректная запись источника отбрасывается и не стопорит опрос остальных.
func TestMalformedSourceRecordIsSkipped(t *testing.T) {
	e := setup(t, 500)
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-BAD", ClinicCode: "C999"})
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-1"})
	res := e.sync(t)
	if res.Applied != 1 || res.Rejected != 1 || res.To != 2 {
		t.Fatalf("опрос с некорректной записью: %+v", res)
	}
}

// Строки единиц и истории статусов не удаляются (прослеживаемость, ПП РФ № 641).
func TestUnitRowsCannotBeDeleted(t *testing.T) {
	e := setup(t, 500)
	e.aist.Add(fakeaist.Unit{AistUnitCode: "U-1"})
	e.sync(t)
	ctx := context.Background()
	for _, q := range []string{
		`DELETE FROM unit_status_history`,
		`UPDATE unit_status_history SET unit_status_code = 'ISSUED'`,
		`DELETE FROM unit`,
		`TRUNCATE unit_status_history`,
	} {
		if _, err := e.pool.Exec(ctx, q); err == nil {
			t.Errorf("%s должен быть запрещён", q)
		}
	}
}

// Демо-публикация защищённого объёма: версия растёт, ключ clinic_id:kind, аудит в той же транзакции.
func TestProtectedVolumeIsPublishedWithGrowingVersion(t *testing.T) {
	e := setup(t, 500)
	ctx := context.Background()
	actor := app.Actor{UserID: "duty", Role: "DUTY_OFFICER"}
	first, err := e.svc.PublishProtectedVolume(ctx, actor, domain.ProtectedVolume{ClinicID: 1, ComponentKind: "RBC", VolumeUnits: 4})
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.svc.PublishProtectedVolume(ctx, actor, domain.ProtectedVolume{ClinicID: 1, ComponentKind: "RBC", VolumeUnits: 6, Reason: "учения"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Version <= first.Version {
		t.Fatalf("версия не растёт: %d, %d", first.Version, second.Version)
	}
	msgs := outbox(t, e.pool)
	if len(msgs) != 2 || msgs[1].Type != "ProtectedVolumePublished" || msgs[1].Key != "1:RBC" ||
		msgs[1].Payload["volume_units"] != float64(6) || msgs[1].Payload["reason"] != "учения" {
		t.Fatalf("outbox: %+v", msgs)
	}
	if first.Reason != app.DemoProtectedVolumeReason {
		t.Fatalf("основание по умолчанию: %q", first.Reason)
	}
	var audits int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_local WHERE action = $1`, app.ActionPublishProtectedVolume).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 2 {
		t.Fatalf("записей аудита %d, ожидали 2", audits)
	}

	for _, bad := range []domain.ProtectedVolume{
		{ClinicID: 99999, ComponentKind: "RBC", VolumeUnits: 1},
		{ClinicID: 1, ComponentKind: "BLOOD", VolumeUnits: 1},
		{ClinicID: 1, ComponentKind: "RBC", VolumeUnits: -1},
	} {
		if _, err := e.svc.PublishProtectedVolume(ctx, actor, bad); !errors.Is(err, app.ErrInvalid) {
			t.Errorf("%+v: ожидали ErrInvalid, получили %v", bad, err)
		}
	}
}
