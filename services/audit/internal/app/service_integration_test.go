//go:build integration

package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/testinfra"
	"github.com/AlchemistBro/blood-supply-pet/services/audit/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/audit/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/audit/migrations"
)

func setup(t *testing.T) (*app.Service, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	pool, err := bspgx.Connect(ctx, testinfra.Postgres(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := bspgx.Migrate(ctx, pool, messaging.Migrations, migrations.FS); err != nil {
		t.Fatal(err)
	}
	svc := app.New(pool, postgres.New(pool), clock.NewMutable(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return svc, pool
}

func record(i int) auditc.AuditRecorded {
	return auditc.AuditRecorded{
		OccurredAt: time.Date(2026, 10, 20, 12, 0, i%60, 0, time.UTC), UserID: "operator-c001", Role: "CLINIC_OPERATOR",
		Module: auditc.ModuleTransfer, Action: "CONFIRM_TRANSFER", ObjectType: "obligation",
		ObjectID: fmt.Sprintf("o-%d", i), Result: auditc.ResultSuccess, Details: map[string]any{"i": i},
	}
}

func withDetails(r auditc.AuditRecorded, d map[string]any) auditc.AuditRecorded {
	r.Details = d
	return r
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// Шестьдесят параллельных записей не рвут цепочку: голова блокируется, seq идёт подряд.
func TestParallelAppendsKeepChainIntact(t *testing.T) {
	svc, _ := setup(t)
	ctx := context.Background()
	const n = 60
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.AppendDirect(ctx, record(i), "test"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("параллельная запись: %v", err)
	}
	rep, err := svc.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK || rep.Checked != n {
		t.Fatalf("цепочка после %d параллельных записей: %+v", n, rep)
	}
}

// Kafka доставляет «хотя бы один раз»: три доставки одного сообщения дают одну запись.
func TestDuplicateDeliveryIsDroppedByInbox(t *testing.T) {
	svc, _ := setup(t)
	ctx := context.Background()
	msg := messaging.Message{ID: uuid.New(), Type: "AuditRecorded", SchemaVersion: 1, Payload: mustJSON(record(1)), Producer: "transfer"}
	for i := 0; i < 3; i++ {
		if err := svc.HandleMessage(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	page, err := svc.List(ctx, app.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("после трёх доставок одного сообщения в журнале %d записей", len(page.Items))
	}
}

// Ядовитые сообщения помечаются постоянной ошибкой: consumer отправит их в DLT без повторов.
func TestPoisonMessagesArePermanent(t *testing.T) {
	svc, _ := setup(t)
	ctx := context.Background()
	cases := map[string]messaging.Message{
		"неизвестный тип":        {ID: uuid.New(), Type: "SomethingElse", Payload: []byte(`{}`)},
		"битый JSON":             {ID: uuid.New(), Type: "AuditRecorded", Payload: []byte(`{`)},
		"нет обязательных полей": {ID: uuid.New(), Type: "AuditRecorded", Payload: []byte(`{"occurred_at":"2026-10-20T12:00:00Z"}`)},
		"номер случая в details": {ID: uuid.New(), Type: "AuditRecorded", Payload: mustJSON(withDetails(record(1), map[string]any{"patient_case_no": "X"}))},
	}
	for name, msg := range cases {
		err := svc.HandleMessage(ctx, msg)
		if err == nil || !messaging.IsPermanent(err) {
			t.Errorf("%s: ожидали постоянную ошибку (в DLT), получили %v", name, err)
		}
	}
}

// Журнал нельзя изменить обычным UPDATE или DELETE: запрещает триггер на уровне СУБД.
func TestJournalCannotBeModifiedByDatabase(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	if _, err := svc.AppendDirect(ctx, record(1), "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE audit_record SET action = 'HACKED'`); err == nil {
		t.Fatal("UPDATE журнала должен быть запрещён триггером")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM audit_record`); err == nil {
		t.Fatal("DELETE журнала должен быть запрещён триггером")
	}
}

// Даже владелец, отключивший триггер и поправивший запись, оставляет след: проверка находит разрыв.
func TestTamperingIsDetectedByVerify(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		if _, err := svc.AppendDirect(ctx, record(i), "test"); err != nil {
			t.Fatal(err)
		}
	}
	for _, stmt := range []string{
		`ALTER TABLE audit_record DISABLE TRIGGER audit_record_no_update`,
		`UPDATE audit_record SET details = '{"i": 999}' WHERE seq = 3`,
		`ALTER TABLE audit_record ENABLE TRIGGER audit_record_no_update`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	rep, err := svc.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.Broken == nil || rep.Broken.Seq != 3 {
		t.Fatalf("подмена записи 3 не обнаружена: %+v", rep)
	}
	if rep.Checked != 2 {
		t.Fatalf("до разрыва проверено %d записей, ожидали 2", rep.Checked)
	}
}

// Время с наносекундами (как time.Now) должно давать цепочку, которую проверка принимает.
// PostgreSQL хранит микросекунды: без усечения при записи хеш расходился с сохранённым.
func TestNanosecondTimestampsStillVerify(t *testing.T) {
	svc, _ := setup(t)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		rec := record(i)
		rec.OccurredAt = time.Now().UTC()
		if _, err := svc.AppendDirect(ctx, rec, "test"); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := svc.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK || rep.Checked != 3 {
		t.Fatalf("цепочка с наносекундными метками не проходит проверку: %+v", rep)
	}
}
