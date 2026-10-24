//go:build integration

package kafka_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testinfra"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/adapters/kafka"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/migrations"
)

// Сага «подтверждение → заказ перевозки» со стороны transfer на настоящих PostgreSQL и Kafka
// (перенос TransportSagaTests.java): команды уходят через outbox и ретранслятор, ответы шлюза приходят
// через consumer и inbox. Шлюз изображает тест; сам шлюз проверяется в своём сервисе.

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type aist struct {
	mu       sync.Mutex
	statuses map[uuid.UUID]domain.UnitStatus
}

func (a *aist) CheckUnits(_ context.Context, ids []uuid.UUID) (app.UnitStatusReport, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := app.UnitStatusReport{Statuses: map[uuid.UUID]domain.UnitStatus{}}
	for _, id := range ids {
		if s, ok := a.statuses[id]; ok {
			out.Statuses[id] = s
		}
	}
	return out, nil
}

func (a *aist) set(id uuid.UUID, s domain.UnitStatus) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.statuses[id] = s
}

type saga struct {
	t        *testing.T
	ctx      context.Context
	pool     *pgxpool.Pool
	svc      *app.Service
	aist     *aist
	clock    *clock.Mutable
	brokers  []string
	replies  string // уникальный топик ответов шлюза этого теста
	versions atomic.Int64
}

func setupSaga(t *testing.T) *saga {
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
	if err := bspgx.WithTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error { return refdata.MustLoad().Seed(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
	brokers := testinfra.Kafka(t)
	replies := testinfra.UniqueTopic("logistics.replies")
	topics := append([]string{replies}, contracts.TopicLogisticsCommands, contracts.TopicTransferEvents, contracts.TopicAuditEvents)
	if err := messaging.EnsureTopics(ctx, brokers, 1, messaging.WithDLT(topics...)...); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewMutable(time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC))
	a := &aist{statuses: map[uuid.UUID]domain.UnitStatus{}}
	svc := app.New(pool, postgres.New(), a, messaging.NewJournal("transfer", auditc.ModuleTransfer, clk), clk, quiet,
		app.Config{ExpiryInterval: 5 * time.Second, ExpiryBatch: 100})
	s := &saga{t: t, ctx: ctx, pool: pool, svc: svc, aist: a, clock: clk, brokers: brokers, replies: replies}
	s.startConsumer()
	return s
}

// startConsumer — consumer ответов шлюза с теми же обработчиками, что в сервисе.
func (s *saga) startConsumer() {
	s.t.Helper()
	c, err := messaging.NewConsumer(messaging.ConsumerConfig{
		Brokers: s.brokers, Group: "transfer-test-" + uuid.NewString(), Topics: []string{s.replies},
		MaxRetries: 2, MinBackoff: 10 * time.Millisecond,
	}, kafka.New(s.svc, quiet).Logistics, quiet)
	if err != nil {
		s.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = c.Run(ctx); close(done) }()
	s.t.Cleanup(func() { cancel(); <-done })
}

func (s *saga) registerUnits(clinic int64, n int) []uuid.UUID {
	s.t.Helper()
	out := make([]uuid.UUID, 0, n)
	for i := 0; i < n; i++ {
		id := uuid.New()
		s.aist.set(id, domain.UnitInStock)
		if err := s.svc.OnUnitRegistered(s.ctx, nil, app.UnitRegistered{
			UnitID: id, ClinicID: clinic, ComponentKind: "RBC", BloodGroup: "O", RhPositive: true, VolumeML: 300,
			ExpiresAt: s.clock.Now().Add(20 * 24 * time.Hour), Version: s.versions.Add(1),
		}); err != nil {
			s.t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func (s *saga) open(clinic int64, qty int) uuid.UUID {
	s.t.Helper()
	id, err := s.svc.Open(s.ctx, nil, domain.NewObligation{
		ChoiceID: uuid.New(), RequestID: uuid.New(), Origin: domain.OriginDoctorChoice, SourceClinicID: clinic,
		TargetClinicID: 100, ComponentKind: "RBC", RequestedQty: qty, UrgencyCode: "URGENT", ConfirmWindow: 15 * time.Minute,
		Spec: domain.ReplacementSpec{RuleVersion: "O15-v3"},
	})
	if err != nil {
		s.t.Fatal(err)
	}
	return id
}

func (s *saga) confirm(id uuid.UUID, units []uuid.UUID, partial bool) domain.ConfirmationResult {
	s.t.Helper()
	res, err := s.svc.Confirm(s.ctx, app.ConfirmTransfer{ObligationID: id, UnitIDs: units, AcceptPartial: partial,
		IdempotencyKey: uuid.NewString(), Operator: "operator-1"})
	if err != nil {
		s.t.Fatal(err)
	}
	return res
}

func (s *saga) confirmedObligation() uuid.UUID {
	s.t.Helper()
	units := s.registerUnits(1, 1)
	o := s.open(1, 1)
	if res := s.confirm(o, units, false); !isConfirmed(res) {
		s.t.Fatalf("подтверждение: %#v", res)
	}
	return o
}

func (s *saga) withdraw(unit uuid.UUID) {
	s.t.Helper()
	s.aist.set(unit, domain.UnitIssued)
	if err := s.svc.OnUnitWithdrawn(s.ctx, nil, app.UnitWithdrawn{UnitID: unit, ClinicID: 1, ComponentKind: "RBC",
		Reason: "ISSUED", Version: s.versions.Add(1)}); err != nil {
		s.t.Fatal(err)
	}
}

// send — ответ шлюза в Kafka, как его пишет ретранслятор logistics-gateway: ключ obligation_id, заголовки.
func (s *saga) send(obligation uuid.UUID, p contracts.Payload, messageID uuid.UUID) {
	s.t.Helper()
	body, err := json.Marshal(p)
	if err != nil {
		s.t.Fatal(err)
	}
	m := messaging.Message{ID: messageID, Type: p.MessageType(), SchemaVersion: p.SchemaVersion(), Topic: s.replies,
		Key: obligation.String(), Payload: body, Producer: "logistics-gateway", OccurredAt: time.Now().UTC()}
	client, err := messaging.NewProducerClient(s.brokers)
	if err != nil {
		s.t.Fatal(err)
	}
	defer client.Close()
	if err := client.ProduceSync(s.ctx, m.Record()).FirstErr(); err != nil {
		s.t.Fatal(err)
	}
}

// barrier — сообщение с тем же ключом идёт в ту же партицию следом: когда оно обработано, обработано всё до него.
func (s *saga) barrier(obligation uuid.UUID) {
	s.t.Helper()
	s.send(obligation, logistics.TransportCancelled{ObligationID: obligation}, uuid.New())
	s.waitFor(func() bool { return slices.Contains(s.audits(obligation), app.ActionTransportCancelled) }, "ответ-барьер")
}

func (s *saga) waitFor(cond func() bool, what string) {
	s.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.t.Fatalf("не дождались: %s", what)
}

func (s *saga) view(id uuid.UUID) app.ObligationView {
	s.t.Helper()
	v, err := s.svc.Find(s.ctx, id)
	if err != nil {
		s.t.Fatal(err)
	}
	return v
}

func (s *saga) strings(sql string, args ...any) []string {
	s.t.Helper()
	rows, err := s.pool.Query(s.ctx, sql, args...)
	if err != nil {
		s.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		s.t.Fatal(err)
	}
	return out
}

func (s *saga) events(id uuid.UUID) []string {
	return s.strings(`SELECT message_type FROM outbox WHERE topic = $1 AND key = $2 ORDER BY id`, contracts.TopicTransferEvents, id.String())
}

func (s *saga) commands(id uuid.UUID) []string {
	return s.strings(`SELECT message_type FROM outbox WHERE topic = $1 AND key = $2 ORDER BY id`, contracts.TopicLogisticsCommands, id.String())
}

func (s *saga) audits(id uuid.UUID) []string {
	return s.strings(`SELECT action FROM audit_local WHERE object_type = 'obligation' AND object_id = $1 ORDER BY id`, id.String())
}

func isConfirmed(r domain.ConfirmationResult) bool {
	_, ok := r.(domain.Confirmed)
	return ok
}

func count(list []string, v string) int {
	n := 0
	for _, x := range list {
		if x == v {
			n++
		}
	}
	return n
}

func TestConfirmationPublishesRequestTransportKeyedByObligation(t *testing.T) {
	s := setupSaga(t)
	units := s.registerUnits(1, 3)
	o := s.open(1, 3)
	if res := s.confirm(o, units[:2], true); !isConfirmed(res) {
		t.Fatalf("подтверждение: %#v", res)
	}
	if got := s.commands(o); !slices.Equal(got, []string{"RequestTransport"}) {
		t.Fatalf("команды: %v", got)
	}

	// Ретранслятор отправляет outbox в Kafka; команда приходит в logistics.commands с ключом obligation_id.
	producer, err := messaging.NewProducerClient(s.brokers)
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	relay := messaging.NewRelay(s.pool, messaging.KafkaProducer{Client: producer}, messaging.RelayConfig{BatchSize: 100}, quiet)
	if _, err := relay.DrainOnce(s.ctx); err != nil {
		t.Fatal(err)
	}
	reader, err := kgo.NewClient(kgo.SeedBrokers(s.brokers...), kgo.ConsumeTopics(contracts.TopicLogisticsCommands),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var found *messaging.Message
	deadline := time.Now().Add(30 * time.Second)
	for found == nil && time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(s.ctx, time.Second)
		fetches := reader.PollFetches(ctx)
		cancel()
		fetches.EachRecord(func(r *kgo.Record) {
			if string(r.Key) != o.String() || found != nil {
				return
			}
			if m, err := messaging.FromRecord(r); err == nil {
				found = &m
			}
		})
	}
	if found == nil {
		t.Fatalf("в %s нет сообщения с ключом %s", contracts.TopicLogisticsCommands, o)
	}
	if found.Type != "RequestTransport" {
		t.Fatalf("тип %s", found.Type)
	}
	var cmd logistics.RequestTransport
	if err := found.Decode(&cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.Quantity != 2 {
		t.Errorf("перевозчику заказано %d, ожидали подтверждённое число 2, а не запрошенное 3", cmd.Quantity)
	}
	if regime, _ := logistics.TemperatureRegimeFor("RBC"); cmd.TemperatureRegime != regime {
		t.Errorf("температурный режим %q, ожидали %q", cmd.TemperatureRegime, regime)
	}
}

func TestAcceptedReplyMovesObligationOnceEvenIfDeliveredTwice(t *testing.T) {
	s := setupSaga(t)
	o := s.confirmedObligation()
	id := uuid.New()
	reply := logistics.TransportAccepted{ObligationID: o, ShipmentRef: "C03-777"}

	s.send(o, reply, id)
	s.send(o, reply, id) // повторная доставка
	s.barrier(o)
	var inboxCount int
	if err := s.pool.QueryRow(s.ctx, `SELECT count(*) FROM inbox WHERE message_id = $1 AND message_type = 'TransportAccepted'`, id).Scan(&inboxCount); err != nil {
		t.Fatal(err)
	}
	if inboxCount != 1 {
		t.Fatalf("сообщение не сохранено в inbox: строк %d, ожидали 1", inboxCount)
	}

	v := s.view(o)
	if v.Status != domain.StatusTransportRequested || v.TransportRef != "C03-777" {
		t.Fatalf("после ответа: %s %q", v.Status, v.TransportRef)
	}
	if got := s.events(o); !slices.Equal(got, []string{"ObligationOpened", "TransferConfirmed", "TransportRequested"}) {
		t.Fatalf("события: %v", got)
	}
	audits := s.audits(o)
	if slices.Contains(audits, app.ActionTransportReplyIgnored) || count(audits, app.ActionTransportRequested) != 1 {
		t.Fatalf("повтор должен отбрасывать inbox, а не CAS: аудит %v", audits)
	}
}

func TestWithdrawalAfterTransportRequestedCompensatesWithCancelTransport(t *testing.T) {
	s := setupSaga(t)
	units := s.registerUnits(1, 2)
	o := s.open(1, 2)
	s.confirm(o, units, false)
	s.send(o, logistics.TransportAccepted{ObligationID: o, ShipmentRef: "C03-1"}, uuid.New())
	s.waitFor(func() bool { return s.view(o).Status == domain.StatusTransportRequested }, "TRANSPORT_REQUESTED")

	s.withdraw(units[0])

	if st := s.view(o).Status; st != domain.StatusBroken {
		t.Fatalf("статус %s, ожидали BROKEN", st)
	}
	if got := s.commands(o); !slices.Equal(got, []string{"RequestTransport", "CancelTransport"}) {
		t.Fatalf("команды: %v", got)
	}
	var reserved int
	if err := s.pool.QueryRow(s.ctx, `SELECT reserved_units FROM source_stock WHERE clinic_id = 1 AND component_kind_code = 'RBC'`).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved != 0 {
		t.Errorf("резерв после нарушения %d, ожидали 0", reserved)
	}
}

func TestLateAcceptanceDoesNotReviveBrokenObligation(t *testing.T) {
	s := setupSaga(t)
	units := s.registerUnits(1, 1)
	o := s.open(1, 1)
	s.confirm(o, units, false)
	s.withdraw(units[0])

	s.send(o, logistics.TransportAccepted{ObligationID: o, ShipmentRef: "C03-2"}, uuid.New())
	s.waitFor(func() bool { return slices.Contains(s.audits(o), app.ActionTransportReplyIgnored) }, "TRANSPORT_REPLY_IGNORED")

	if st := s.view(o).Status; st != domain.StatusBroken {
		t.Fatalf("поздний ответ оживил задание: %s", st)
	}
	if got := s.commands(o); !slices.Equal(got, []string{"RequestTransport", "CancelTransport"}) {
		t.Fatalf("компенсация ушла в момент нарушения, других команд нет: %v", got)
	}
}

func TestUnknownOutcomeKeepsConfirmedAndLateAcceptanceStillLands(t *testing.T) {
	s := setupSaga(t)
	o := s.confirmedObligation()

	s.send(o, logistics.TransportRejected{ObligationID: o, Reason: logistics.RejectOutcomeUnknown, Details: "C03 недоступна"}, uuid.New())
	s.waitFor(func() bool { return slices.Contains(s.events(o), "EscalationRaised") }, "эскалация дежурному")
	if st := s.view(o).Status; st != domain.StatusConfirmed {
		t.Fatalf("неизвестный исход: %s, единицы должны остаться закреплены (CONFIRMED)", st)
	}

	s.send(o, logistics.TransportAccepted{ObligationID: o, ShipmentRef: "C03-3"}, uuid.New())
	s.waitFor(func() bool { return s.view(o).Status == domain.StatusTransportRequested }, "запоздалый TransportAccepted")
}
