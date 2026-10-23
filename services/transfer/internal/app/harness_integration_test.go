//go:build integration

package app_test

import (
	"context"
	"io"
	"log/slog"
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

// Обвязка сценариев: своя база на тест (testinfra), поэтому клиники из справочника можно переиспользовать.
const (
	rule   = "O15-v3"
	target = int64(100)
)

var start = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)

// fakeAIST — управляемый ответ inventory на точечную проверку статуса (ADR-0004).
type fakeAIST struct {
	mu        sync.Mutex
	statuses  map[uuid.UUID]domain.UnitStatus
	down      bool
	fromCopy  bool
	callCount int
}

func (f *fakeAIST) CheckUnits(_ context.Context, ids []uuid.UUID) (app.UnitStatusReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	if f.down {
		return app.UnitStatusReport{}, app.ErrSourceSystemUnavailable
	}
	out := app.UnitStatusReport{Statuses: map[uuid.UUID]domain.UnitStatus{}, FromCopy: f.fromCopy}
	for _, id := range ids {
		if s, ok := f.statuses[id]; ok {
			out.Statuses[id] = s
		}
	}
	return out, nil
}

func (f *fakeAIST) set(id uuid.UUID, s domain.UnitStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses[id] = s
}

func (f *fakeAIST) setDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

func (f *fakeAIST) setFromCopy(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fromCopy = v
}

type harness struct {
	t        *testing.T
	ctx      context.Context
	svc      *app.Service
	pool     *pgxpool.Pool
	clock    *clock.Mutable
	aist     *fakeAIST
	versions atomic.Int64
}

func setup(t *testing.T) *harness {
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
	if err := bspgx.WithTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		return refdata.MustLoad().Seed(ctx, tx)
	}); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewMutable(start)
	aist := &fakeAIST{statuses: map[uuid.UUID]domain.UnitStatus{}}
	journal := messaging.NewJournal("transfer", auditc.ModuleTransfer, clk)
	svc := app.New(pool, postgres.New(), aist, journal, clk, slog.New(slog.NewTextHandler(io.Discard, nil)),
		app.Config{ExpiryInterval: 5 * time.Second, ExpiryBatch: 100})
	return &harness{t: t, ctx: ctx, svc: svc, pool: pool, clock: clk, aist: aist}
}

// delivery — входящее сообщение Kafka с новым message-id (для inbox).
func delivery() *messaging.Message {
	return &messaging.Message{ID: uuid.New(), Type: "test", SchemaVersion: 1}
}

func ptr[T any](v T) *T { return &v }

func (h *harness) inDays(d int) time.Time {
	return h.clock.Now().Add(time.Duration(d) * 24 * time.Hour)
}

func (h *harness) registerUnit(clinic int64, kind, group string, rh bool, expires time.Time) uuid.UUID {
	h.t.Helper()
	id := uuid.New()
	h.aist.set(id, domain.UnitInStock)
	if err := h.svc.OnUnitRegistered(h.ctx, delivery(), app.UnitRegistered{
		UnitID: id, ClinicID: clinic, ComponentKind: kind, BloodGroup: group, RhPositive: rh,
		VolumeML: 300, ExpiresAt: expires, Version: h.versions.Add(1),
	}); err != nil {
		h.t.Fatalf("регистрация единицы: %v", err)
	}
	return id
}

func (h *harness) registerUnits(clinic int64, kind string, n int) []uuid.UUID {
	h.t.Helper()
	groups := []string{"O", "A", "B", "AB"}
	out := make([]uuid.UUID, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, h.registerUnit(clinic, kind, groups[i%len(groups)], i%2 == 0, h.inDays(20)))
	}
	return out
}

func (h *harness) protect(clinic int64, kind string, volume int) {
	h.t.Helper()
	if err := h.svc.OnProtectedVolume(h.ctx, delivery(), app.ProtectedVolume{
		ClinicID: clinic, ComponentKind: kind, VolumeUnits: volume, Version: h.versions.Add(1), Reason: "норматив FR012 + фоновая потребность, демо",
	}); err != nil {
		h.t.Fatalf("защищённый объём: %v", err)
	}
}

func (h *harness) withdraw(unit uuid.UUID, clinic int64, kind string) {
	h.t.Helper()
	h.aist.set(unit, domain.UnitIssued)
	if err := h.svc.OnUnitWithdrawn(h.ctx, delivery(), app.UnitWithdrawn{
		UnitID: unit, ClinicID: clinic, ComponentKind: kind, Reason: "ISSUED", Version: h.versions.Add(1),
	}); err != nil {
		h.t.Fatalf("выбытие единицы: %v", err)
	}
}

func (h *harness) quarantine(unit uuid.UUID, clinic int64, kind string) {
	h.t.Helper()
	h.aist.set(unit, domain.UnitQuarantine)
	if err := h.svc.OnUnitUpdated(h.ctx, delivery(), app.UnitUpdated{
		UnitID: unit, ClinicID: clinic, ComponentKind: kind, Status: domain.UnitQuarantine, Version: h.versions.Add(1),
	}); err != nil {
		h.t.Fatalf("карантин единицы: %v", err)
	}
}

func newObligation(source int64, kind string, qty int, fallback []int64, spec domain.ReplacementSpec) domain.NewObligation {
	return domain.NewObligation{
		ChoiceID: uuid.New(), RequestID: uuid.New(), Origin: domain.OriginDoctorChoice,
		SourceClinicID: source, TargetClinicID: target, ComponentKind: kind, RequestedQty: qty,
		UrgencyCode: "URGENT", ConfirmWindow: 15 * time.Minute, FallbackSources: fallback, Spec: spec,
	}
}

func (h *harness) openWith(n domain.NewObligation) uuid.UUID {
	h.t.Helper()
	id, err := h.svc.Open(h.ctx, nil, n)
	if err != nil {
		h.t.Fatalf("открытие задания: %v", err)
	}
	return id
}

func (h *harness) open(source int64, kind string, qty int) uuid.UUID {
	return h.openWith(newObligation(source, kind, qty, nil, domain.ReplacementSpec{RuleVersion: rule}))
}

func (h *harness) confirm(id uuid.UUID, units []uuid.UUID, partial bool) domain.ConfirmationResult {
	h.t.Helper()
	res, err := h.svc.Confirm(h.ctx, app.ConfirmTransfer{
		ObligationID: id, UnitIDs: units, AcceptPartial: partial, IdempotencyKey: uuid.NewString(), Operator: "operator-1",
	})
	if err != nil {
		h.t.Fatalf("подтверждение: %v", err)
	}
	return res
}

func (h *harness) replace(id, old, repl uuid.UUID) domain.ReplacementResult {
	h.t.Helper()
	res, err := h.svc.Replace(h.ctx, app.ReplaceUnit{ObligationID: id, OldUnitID: old, NewUnitID: repl, Operator: "operator-1"})
	if err != nil {
		h.t.Fatalf("замена: %v", err)
	}
	return res
}

func (h *harness) cancel(id uuid.UUID) app.CancelResult {
	h.t.Helper()
	res, err := h.svc.Cancel(h.ctx, nil, app.CancelObligation{ObligationID: id, Reason: "REQUEST_CANCELLED", Actor: "matching"})
	if err != nil {
		h.t.Fatalf("отмена: %v", err)
	}
	return res
}

func (h *harness) reply(r app.LogisticsReply) {
	h.t.Helper()
	if err := h.svc.OnLogisticsReply(h.ctx, delivery(), r); err != nil {
		h.t.Fatalf("ответ логистики: %v", err)
	}
}

func (h *harness) view(id uuid.UUID) app.ObligationView {
	h.t.Helper()
	v, err := h.svc.Find(h.ctx, id)
	if err != nil {
		h.t.Fatalf("чтение задания %s: %v", id, err)
	}
	return v
}

func (h *harness) strings(sql string, args ...any) []string {
	h.t.Helper()
	rows, err := h.pool.Query(h.ctx, sql, args...)
	if err != nil {
		h.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *harness) count(sql string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.pool.QueryRow(h.ctx, sql, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

// eventTypes — доменные события O07 в outbox (transfer.events, ключ obligation_id), по порядку.
func (h *harness) eventTypes(id uuid.UUID) []string {
	return h.strings(`SELECT message_type FROM outbox WHERE topic = $1 AND key = $2 ORDER BY id`, contracts.TopicTransferEvents, id.String())
}

// commandTypes — команды саги шлюзу логистики в outbox.
func (h *harness) commandTypes(id uuid.UUID) []string {
	return h.strings(`SELECT message_type FROM outbox WHERE topic = $1 AND key = $2 ORDER BY id`, contracts.TopicLogisticsCommands, id.String())
}

// payloadField — поле тела последнего сообщения типа msgType по обязательству (события и команды).
func (h *harness) payloadField(id uuid.UUID, msgType, field string) string {
	h.t.Helper()
	var v *string
	if err := h.pool.QueryRow(h.ctx, `SELECT payload ->> $3::text FROM outbox WHERE key = $1 AND message_type = $2 ORDER BY id DESC LIMIT 1`,
		id.String(), msgType, field).Scan(&v); err != nil {
		h.t.Fatalf("%s.%s: %v", msgType, field, err)
	}
	if v == nil {
		return ""
	}
	return *v
}

// auditActions — действия локального аудита по объекту.
func (h *harness) auditActions(id uuid.UUID) []string {
	return h.strings(`SELECT action FROM audit_local WHERE object_type = 'obligation' AND object_id = $1 ORDER BY id`, id.String())
}

func (h *harness) outboxCount() int { return h.count(`SELECT count(*) FROM outbox`) }

// assertStockConsistent — счётчики Остатка совпадают с пересчётом по копии и закреплениям.
func (h *harness) assertStockConsistent(clinic int64, kind string) {
	h.t.Helper()
	var available, reserved int
	if err := h.pool.QueryRow(h.ctx, `SELECT available_units, reserved_units FROM source_stock WHERE clinic_id = $1 AND component_kind_code = $2`,
		clinic, kind).Scan(&available, &reserved); err != nil {
		h.t.Fatal(err)
	}
	inStock := h.count(`SELECT count(*) FROM unit_copy WHERE clinic_id = $1 AND component_kind_code = $2 AND unit_status_code = 'IN_STOCK'`, clinic, kind)
	confirmed := h.count(`SELECT count(*) FROM allocation a JOIN unit_copy u USING (unit_id)
		WHERE u.clinic_id = $1 AND u.component_kind_code = $2 AND a.status = 'CONFIRMED'`, clinic, kind)
	if available != inStock {
		h.t.Errorf("available_units = %d, единиц в наличии %d", available, inStock)
	}
	if reserved != confirmed {
		h.t.Errorf("reserved_units = %d, действующих закреплений %d", reserved, confirmed)
	}
}

func equal[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (h *harness) expectTypes(what string, got []string, want ...string) {
	h.t.Helper()
	if !equal(got, want) {
		h.t.Errorf("%s: %v, ожидали %v", what, got, want)
	}
}
