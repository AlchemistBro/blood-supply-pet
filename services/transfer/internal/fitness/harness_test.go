//go:build integration

package fitness

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testinfra"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/migrations"
)

const (
	rule = "O15-v3"
	// poolSize меньше числа горутин гонки, как Hikari 30 при 50 потоках в прототипе: часть подтверждений
	// ждёт соединения, и порядок входа в транзакцию перемешивается сильнее.
	poolSize = 30
	// raceTimeout — предел одной гонки; зависание означает взаимную блокировку.
	raceTimeout = 60 * time.Second
)

var start = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)

// aist — фальшивый UnitStatusChecker. afterCheck вызывается после того, как статус «получен», и до
// возврата в Confirm/Replace, то есть ровно между проверкой АИСТ-М и транзакцией подтверждения.
type aist struct {
	mu         sync.Mutex
	statuses   map[uuid.UUID]domain.UnitStatus
	afterCheck func(ids []uuid.UUID)
}

func (a *aist) CheckUnits(_ context.Context, ids []uuid.UUID) (app.UnitStatusReport, error) {
	a.mu.Lock()
	out := app.UnitStatusReport{Statuses: map[uuid.UUID]domain.UnitStatus{}}
	for _, id := range ids {
		if s, ok := a.statuses[id]; ok {
			out.Statuses[id] = s
		}
	}
	hook := a.afterCheck
	a.mu.Unlock()
	if hook != nil {
		hook(ids)
	}
	return out, nil
}

func (a *aist) set(id uuid.UUID, s domain.UnitStatus) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.statuses[id] = s
}

func (a *aist) setHook(h func(ids []uuid.UUID)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.afterCheck = h
}

type harness struct {
	t        *testing.T
	ctx      context.Context
	svc      *app.Service
	pool     *pgxpool.Pool
	clock    *clock.Mutable
	aist     *aist
	versions atomic.Int64
}

func setup(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	url := testinfra.Postgres(t)
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	pool, err := bspgx.Connect(ctx, url+sep+fmt.Sprintf("pool_max_conns=%d", poolSize))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := bspgx.Migrate(ctx, pool, messaging.Migrations, refdata.Migrations, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := bspgx.WithTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error { return refdata.MustLoad().Seed(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewMutable(start)
	a := &aist{statuses: map[uuid.UUID]domain.UnitStatus{}}
	svc := app.New(pool, postgres.New(), a, messaging.NewJournal("transfer", auditc.ModuleTransfer, clk), clk,
		slog.New(slog.NewTextHandler(io.Discard, nil)), app.Config{ExpiryInterval: 5 * time.Second, ExpiryBatch: 100})
	return &harness{t: t, ctx: ctx, svc: svc, pool: pool, clock: clk, aist: a}
}

// race запускает задачи одновременно (общий старт после готовности всех горутин) и собирает результаты
// по порядку задач. Ошибка любой задачи или превышение raceTimeout валит тест.
func race(t *testing.T, tasks ...func() (any, error)) []any {
	t.Helper()
	results := make([]any, len(tasks))
	errs := make([]error, len(tasks))
	var ready, done sync.WaitGroup
	startGate := make(chan struct{})
	ready.Add(len(tasks))
	done.Add(len(tasks))
	for i, task := range tasks {
		go func() {
			defer done.Done()
			ready.Done()
			<-startGate
			results[i], errs[i] = task()
		}()
	}
	ready.Wait()
	close(startGate)
	finished := make(chan struct{})
	go func() { done.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(raceTimeout):
		t.Fatalf("гонка не завершилась за %s: вероятна взаимная блокировка", raceTimeout)
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("задача %d гонки: %v", i, err)
		}
	}
	return results
}

func (h *harness) registerUnits(clinic int64, kind string, n int) []uuid.UUID {
	h.t.Helper()
	groups := []string{"O", "A", "B", "AB"}
	out := make([]uuid.UUID, 0, n)
	for i := 0; i < n; i++ {
		id := uuid.New()
		h.aist.set(id, domain.UnitInStock)
		if err := h.svc.OnUnitRegistered(h.ctx, nil, app.UnitRegistered{
			UnitID: id, ClinicID: clinic, ComponentKind: kind, BloodGroup: groups[i%len(groups)], RhPositive: i%2 == 0,
			VolumeML: 300, ExpiresAt: start.Add(20 * 24 * time.Hour), Version: h.versions.Add(1),
		}); err != nil {
			h.t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func (h *harness) protect(clinic int64, kind string, volume int) {
	h.t.Helper()
	if err := h.svc.OnProtectedVolume(h.ctx, nil, app.ProtectedVolume{
		ClinicID: clinic, ComponentKind: kind, VolumeUnits: volume, Version: h.versions.Add(1), Reason: "норматив FR012, демо",
	}); err != nil {
		h.t.Fatal(err)
	}
}

// withdraw — выдача в АИСТ-М и событие UnitWithdrawn в проекцию transfer.
func (h *harness) withdraw(unit uuid.UUID, clinic int64, kind string) error {
	h.aist.set(unit, domain.UnitIssued)
	return h.svc.OnUnitWithdrawn(h.ctx, nil, app.UnitWithdrawn{
		UnitID: unit, ClinicID: clinic, ComponentKind: kind, Reason: "ISSUED", Version: h.versions.Add(1),
	})
}

// quarantine — карантин в АИСТ-М и событие UnitUpdated(QUARANTINE).
func (h *harness) quarantine(unit uuid.UUID, clinic int64, kind string) error {
	h.aist.set(unit, domain.UnitQuarantine)
	return h.svc.OnUnitUpdated(h.ctx, nil, app.UnitUpdated{
		UnitID: unit, ClinicID: clinic, ComponentKind: kind, Status: domain.UnitQuarantine, Version: h.versions.Add(1),
	})
}

func (h *harness) open(source int64, kind string, qty int, fallback ...int64) uuid.UUID {
	h.t.Helper()
	id, err := h.svc.Open(h.ctx, nil, domain.NewObligation{
		ChoiceID: uuid.New(), RequestID: uuid.New(), Origin: domain.OriginDoctorChoice, SourceClinicID: source,
		TargetClinicID: 100, ComponentKind: kind, RequestedQty: qty, UrgencyCode: "URGENT", ConfirmWindow: 15 * time.Minute,
		FallbackSources: fallback, Spec: domain.ReplacementSpec{RuleVersion: rule},
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return id
}

func (h *harness) confirm(id uuid.UUID, units []uuid.UUID, partial bool) (domain.ConfirmationResult, error) {
	return h.svc.Confirm(h.ctx, app.ConfirmTransfer{ObligationID: id, UnitIDs: units, AcceptPartial: partial,
		IdempotencyKey: uuid.NewString(), Operator: "operator-1"})
}

func (h *harness) mustConfirm(id uuid.UUID, units []uuid.UUID, partial bool) domain.ConfirmationResult {
	h.t.Helper()
	res, err := h.confirm(id, units, partial)
	if err != nil {
		h.t.Fatal(err)
	}
	return res
}

func (h *harness) view(id uuid.UUID) app.ObligationView {
	h.t.Helper()
	v, err := h.svc.Find(h.ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return v
}

func (h *harness) count(sql string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.pool.QueryRow(h.ctx, sql, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *harness) eventTypes(id uuid.UUID) []string {
	h.t.Helper()
	rows, err := h.pool.Query(h.ctx, `SELECT message_type FROM outbox WHERE topic = $1 AND key = $2 ORDER BY id`,
		contracts.TopicTransferEvents, id.String())
	if err != nil {
		h.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		h.t.Fatal(err)
	}
	return out
}

// assertStockConsistent — счётчики Остатка совпадают с пересчётом по копии и закреплениям.
func (h *harness) assertStockConsistent(clinic int64, kind string) {
	h.t.Helper()
	var available, reserved int
	if err := h.pool.QueryRow(h.ctx, `SELECT available_units, reserved_units FROM source_stock WHERE clinic_id = $1 AND component_kind_code = $2`,
		clinic, kind).Scan(&available, &reserved); err != nil {
		h.t.Fatal(err)
	}
	inStock := h.count(`SELECT count(*) FROM unit_copy u JOIN ref_unit_status s USING (unit_status_code)
		WHERE u.clinic_id = $1 AND u.component_kind_code = $2 AND s.is_available`, clinic, kind)
	confirmed := h.count(`SELECT count(*) FROM allocation a JOIN unit_copy u USING (unit_id)
		WHERE u.clinic_id = $1 AND u.component_kind_code = $2 AND a.status = 'CONFIRMED'`, clinic, kind)
	if available != inStock {
		h.t.Errorf("Остаток %d/%s: available_units = %d, в наличии по копии %d", clinic, kind, available, inStock)
	}
	if reserved != confirmed {
		h.t.Errorf("Остаток %d/%s: reserved_units = %d, действующих закреплений %d", clinic, kind, reserved, confirmed)
	}
}

// confirmedOnUnavailableUnits — действующие закрепления единиц, которые по копии недоступны (выбыли, карантин).
func (h *harness) confirmedOnUnavailableUnits() int {
	return h.count(`SELECT count(*) FROM allocation a JOIN unit_copy u USING (unit_id) JOIN ref_unit_status s USING (unit_status_code)
		WHERE a.status = 'CONFIRMED' AND NOT s.is_available`)
}

func countOf[T any](results []any) int {
	n := 0
	for _, r := range results {
		if _, ok := r.(T); ok {
			n++
		}
	}
	return n
}
