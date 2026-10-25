//go:build integration

package messaging_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/testinfra"
)

var testLog = slog.New(slog.NewTextHandler(io.Discard, nil))

type env struct {
	db      *pgxpool.Pool
	brokers []string
	topic   string
	journal *messaging.Journal
	clk     *clock.Mutable
}

func setup(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	url := testinfra.Postgres(t)
	pool, err := bspgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := bspgx.Migrate(ctx, pool, messaging.Migrations); err != nil {
		t.Fatal(err)
	}
	brokers := testinfra.Kafka(t)
	topic := testinfra.UniqueTopic("test.events")
	if err := messaging.EnsureTopics(ctx, brokers, 1, messaging.WithDLT(topic)...); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewMutable(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	return &env{db: pool, brokers: brokers, topic: topic, journal: messaging.NewJournal("test-svc", audit.ModuleAudit, clk), clk: clk}
}

func (e *env) publish(t *testing.T, n int, key string) {
	t.Helper()
	ctx := context.Background()
	err := bspgx.WithTx(ctx, e.db, func(ctx context.Context, tx pgx.Tx) error {
		for i := 0; i < n; i++ {
			rec := audit.AuditRecorded{OccurredAt: e.clk.Now(), Module: audit.ModuleAudit, Action: fmt.Sprintf("STEP_%02d", i),
				ObjectType: "test", ObjectID: key, Result: audit.ResultSuccess}
			if _, err := e.journal.Publish(ctx, tx, e.topic, key, rec); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *env) unpublished(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(context.Background(), `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// collector — consumer, который складывает полученные действия по порядку и считает дубли через inbox.
type collector struct {
	mu         sync.Mutex
	actions    []string
	dupes      int
	handled    int
	db         *pgxpool.Pool
	clk        clock.Clock
	failWith   error
	failAction string
}

func (c *collector) handle(ctx context.Context, msg messaging.Message) error {
	if c.failWith != nil {
		var rec audit.AuditRecorded
		if err := msg.Decode(&rec); err != nil {
			return err
		}
		if c.failAction == "" || rec.Action == c.failAction {
			return c.failWith
		}
	}
	return bspgx.WithTx(ctx, c.db, func(ctx context.Context, tx pgx.Tx) error {
		first, err := messaging.FirstTime(ctx, tx, msg, c.clk.Now())
		if err != nil {
			return err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		c.handled++
		if !first {
			c.dupes++
			return nil
		}
		var rec audit.AuditRecorded
		if err := msg.Decode(&rec); err != nil {
			return err
		}
		c.actions = append(c.actions, rec.Action)
		return nil
	})
}

func (c *collector) snapshot() (actions []string, dupes, handled int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.actions...), c.dupes, c.handled
}

func startConsumer(t *testing.T, e *env, group string, c *collector) {
	t.Helper()
	cons, err := messaging.NewConsumer(messaging.ConsumerConfig{Brokers: e.brokers, Group: group, Topics: []string{e.topic}, MaxRetries: 2, MinBackoff: 10 * time.Millisecond}, c.handle, testLog)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = cons.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func waitFor(t *testing.T, d time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("не дождались: %s", what)
}

func TestOutboxRelayInboxRoundTrip(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.publish(t, 20, "agg-1")
	if got := e.unpublished(t); got != 20 {
		t.Fatalf("в outbox %d неопубликованных, ожидали 20", got)
	}

	producer, err := messaging.NewProducerClient(e.brokers)
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	relay := messaging.NewRelay(e.db, messaging.KafkaProducer{Client: producer}, messaging.RelayConfig{BatchSize: 7}, testLog)
	total := 0
	for {
		n, err := relay.DrainOnce(ctx)
		if err != nil {
			t.Fatal(err)
		}
		total += n
		if n < 7 {
			break
		}
	}
	if total != 20 || e.unpublished(t) != 0 {
		t.Fatalf("отправлено %d, осталось %d", total, e.unpublished(t))
	}

	c := &collector{db: e.db, clk: e.clk}
	startConsumer(t, e, "g-roundtrip", c)
	waitFor(t, 60*time.Second, func() bool { a, _, _ := c.snapshot(); return len(a) == 20 }, "20 сообщений у получателя")
	actions, dupes, _ := c.snapshot()
	for i, a := range actions {
		if a != fmt.Sprintf("STEP_%02d", i) {
			t.Fatalf("порядок нарушен на позиции %d: %v", i, actions)
		}
	}
	if dupes != 0 {
		t.Fatalf("дубли без повторной доставки: %d", dupes)
	}

	// Повторная доставка того же сообщения (Kafka «хотя бы один раз»): inbox отбрасывает.
	var raw []byte
	var id string
	if err := e.db.QueryRow(ctx, `SELECT message_id::text, payload FROM outbox ORDER BY id LIMIT 1`).Scan(&id, &raw); err != nil {
		t.Fatal(err)
	}
	rec := &kgo.Record{Topic: e.topic, Key: []byte("agg-1"), Value: raw, Headers: []kgo.RecordHeader{
		{Key: messaging.HeaderMessageID, Value: []byte(id)}, {Key: messaging.HeaderMessageType, Value: []byte("AuditRecorded")}}}
	if err := producer.ProduceSync(ctx, rec).FirstErr(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, func() bool { _, d, _ := c.snapshot(); return d == 1 }, "повтор отброшен inbox")
	actions, _, _ = c.snapshot()
	if len(actions) != 20 {
		t.Fatalf("после повтора обработанных %d", len(actions))
	}
}

// flakyProducer отправляет в Kafka по-настоящему, но «теряет подтверждение» у выбранных сообщений:
// так выглядит падение ретранслятора между отправкой и отметкой published_at.
type flakyProducer struct {
	real     messaging.Producer
	mu       sync.Mutex
	failAt   map[string]bool // message-type → вернуть ошибку после реальной отправки
	refuseAt map[string]bool // message-type → не отправлять вовсе
	produced []string
}

func (p *flakyProducer) ProduceSync(ctx context.Context, rec *kgo.Record) error {
	typ := header(rec, messaging.HeaderMessageType)
	p.mu.Lock()
	refuse := p.refuseAt[typ]
	fail := p.failAt[typ]
	p.mu.Unlock()
	if refuse {
		return errors.New("брокер недоступен")
	}
	if err := p.real.ProduceSync(ctx, rec); err != nil {
		return err
	}
	p.mu.Lock()
	p.produced = append(p.produced, header(rec, messaging.HeaderMessageID))
	p.mu.Unlock()
	if fail {
		return errors.New("подтверждение потеряно")
	}
	return nil
}

func header(rec *kgo.Record, key string) string {
	for _, h := range rec.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

type typed struct{ name string }

func (p typed) MessageType() string { return p.name }
func (p typed) SchemaVersion() int  { return 1 }
func (p typed) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(`{"occurred_at":"2026-10-20T12:00:00Z","module":"audit","action":"%s","object_type":"t","object_id":"x","result":"SUCCESS"}`, p.name)), nil
}

func (e *env) publishTyped(t *testing.T, types ...string) {
	t.Helper()
	err := bspgx.WithTx(context.Background(), e.db, func(ctx context.Context, tx pgx.Tx) error {
		for _, typ := range types {
			if _, err := e.journal.Publish(ctx, tx, e.topic, "agg-2", typed{name: typ}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRelayStopsOnFirstErrorAndKeepsOrder(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.publishTyped(t, "M1", "M2", "M3", "M4", "M5")
	real, err := messaging.NewProducerClient(e.brokers)
	if err != nil {
		t.Fatal(err)
	}
	defer real.Close()
	fp := &flakyProducer{real: messaging.KafkaProducer{Client: real}, refuseAt: map[string]bool{"M3": true}, failAt: map[string]bool{}}
	relay := messaging.NewRelay(e.db, fp, messaging.RelayConfig{BatchSize: 100}, testLog)

	n, err := relay.DrainOnce(ctx)
	if err == nil || n != 2 {
		t.Fatalf("ожидали остановку после двух: n=%d err=%v", n, err)
	}
	if e.unpublished(t) != 3 {
		t.Fatalf("после остановки неопубликованных %d, ожидали 3", e.unpublished(t))
	}
	fp.mu.Lock()
	fp.refuseAt = map[string]bool{}
	fp.mu.Unlock()
	n, err = relay.DrainOnce(ctx)
	if err != nil || n != 3 || e.unpublished(t) != 0 {
		t.Fatalf("после восстановления: n=%d err=%v left=%d", n, err, e.unpublished(t))
	}

	c := &collector{db: e.db, clk: e.clk}
	startConsumer(t, e, "g-order", c)
	waitFor(t, 60*time.Second, func() bool { a, _, _ := c.snapshot(); return len(a) == 5 }, "5 сообщений")
	actions, _, _ := c.snapshot()
	for i, a := range actions {
		if a != fmt.Sprintf("M%d", i+1) {
			t.Fatalf("порядок нарушен: %v", actions)
		}
	}
}

func TestRelayCrashBetweenSendAndMarkIsDeduplicatedByInbox(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.publishTyped(t, "A1", "A2", "A3")
	real, err := messaging.NewProducerClient(e.brokers)
	if err != nil {
		t.Fatal(err)
	}
	defer real.Close()
	// A2 отправлено в Kafka, но подтверждение «потеряно»: ретранслятор падает до отметки published_at.
	fp := &flakyProducer{real: messaging.KafkaProducer{Client: real}, failAt: map[string]bool{"A2": true}, refuseAt: map[string]bool{}}
	relay := messaging.NewRelay(e.db, fp, messaging.RelayConfig{BatchSize: 100}, testLog)
	if n, err := relay.DrainOnce(ctx); err == nil || n != 1 {
		t.Fatalf("ожидали падение на A2: n=%d err=%v", n, err)
	}
	// «Перезапуск»: подтверждения больше не теряются, A2 уйдёт второй раз.
	fp.mu.Lock()
	fp.failAt = map[string]bool{}
	fp.mu.Unlock()
	if n, err := relay.DrainOnce(ctx); err != nil || n != 2 {
		t.Fatalf("после перезапуска: n=%d err=%v", n, err)
	}
	fp.mu.Lock()
	produced := len(fp.produced)
	fp.mu.Unlock()
	if produced != 4 {
		t.Fatalf("в Kafka должно уйти 4 записи (A2 дважды), ушло %d", produced)
	}

	c := &collector{db: e.db, clk: e.clk}
	startConsumer(t, e, "g-crash", c)
	waitFor(t, 60*time.Second, func() bool { _, _, h := c.snapshot(); return h == 4 }, "4 доставки")
	actions, dupes, _ := c.snapshot()
	if len(actions) != 3 || dupes != 1 {
		t.Fatalf("обработано %v, дублей %d: inbox должен отбросить ровно один", actions, dupes)
	}
}

func TestConsumerSendsPoisonToDLT(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.publishTyped(t, "P1", "A2")
	real, err := messaging.NewProducerClient(e.brokers)
	if err != nil {
		t.Fatal(err)
	}
	defer real.Close()
	relay := messaging.NewRelay(e.db, messaging.KafkaProducer{Client: real}, messaging.RelayConfig{}, testLog)
	if _, err := relay.DrainOnce(ctx); err != nil {
		t.Fatal(err)
	}

	c := &collector{db: e.db, clk: e.clk, failWith: messaging.Permanent(errors.New("неизвестный тип")), failAction: "P1"}
	startConsumer(t, e, "g-dlt", c)

	dlt, err := kgo.NewClient(kgo.SeedBrokers(e.brokers...), kgo.ConsumeTopics(e.topic+contracts.DLTSuffix), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal(err)
	}
	defer dlt.Close()
	pollCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	fetches := dlt.PollRecords(pollCtx, 1)
	if fetches.Err() != nil && !errors.Is(fetches.Err(), context.DeadlineExceeded) {
		t.Fatal(fetches.Err())
	}
	recs := fetches.Records()
	if len(recs) != 1 {
		t.Fatalf("в DLT %d записей, ожидали 1", len(recs))
	}
	if header(recs[0], messaging.HeaderDLTError) == "" || header(recs[0], messaging.HeaderDLTTopic) != e.topic {
		t.Fatalf("заголовки DLT не заполнены: %+v", recs[0].Headers)
	}
	waitFor(t, 60*time.Second, func() bool {
		actions, _, _ := c.snapshot()
		return len(actions) == 1
	}, "корректное сообщение после ядовитого")
	actions, _, handled := c.snapshot()
	if handled != 1 || actions[0] != "A2" {
		t.Fatalf("после DLT обработано %v (%d доставок), ожидали только A2", actions, handled)
	}
}
