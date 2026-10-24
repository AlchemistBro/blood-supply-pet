//go:build integration

// Шлюз логистики против дублей и падений посреди запроса (перенос TransportGatewayTests прототипа).
// C03 здесь — настоящий sim-logistics, собранный и запущенный отдельным процессом: импортировать пакеты
// другого сервиса запрещают компилятор (internal) и tools/archtest. Имитатор не дедуплицирует заказы,
// поэтому «ровно один заказ у перевозчика» в этих тестах держит только шлюз.
//
// Тесты идут последовательно: имитатор общий, его сбои глобальные, перед каждым тестом /dev/reset.
package app_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/testinfra"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/adapters/c03"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/adapters/kafka"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/domain"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/migrations"
)

const simPackage = "github.com/AlchemistBro/blood-supply-pet/services/sim-logistics/cmd/sim-logistics"

// simURL — адрес запущенного имитатора C03.
var simURL string

var start = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

// simHTTP — клиент пульта имитатора: с таймаутом, чтобы зависший имитатор ронял тест, а не вешал его.
var simHTTP = &http.Client{Timeout: 10 * time.Second}

// runTests собирает и запускает sim-logistics, ждёт /healthz и гоняет тесты.
//
// Порт выбирает сам имитатор (127.0.0.1:0) и печатает «listening on <адрес>»: гонки за свободный порт нет.
// Имитатор слушает stdin и завершается по его закрытию: если тестовый процесс упадёт (паника в тесте, таймаут
// go test, kill), ОС закроет трубу, и имитатор не останется висеть. Паника в главной горутине перехватывается,
// чтобы отложенная остановка имитатора выполнилась.
func runTests(m *testing.M) (code int) {
	dir, err := os.MkdirTemp("", "sim-logistics-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "временный каталог:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	bin := filepath.Join(dir, "sim-logistics")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, simPackage)
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "сборка sim-logistics:", err)
		return 1
	}

	sim := exec.Command(bin)
	sim.Env = append(os.Environ(), "BS_HTTP_ADDR=127.0.0.1:0", "BS_PRINT_ADDR=true", "BS_EXIT_ON_STDIN_EOF=true",
		"BS_LOG_LEVEL=warn", "BS_LOG_FORMAT=text", "BS_OTEL_ENDPOINT=")
	sim.Stderr = os.Stderr
	stdin, err := sim.StdinPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, "stdin sim-logistics:", err)
		return 1
	}
	stdout, err := sim.StdoutPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, "stdout sim-logistics:", err)
		return 1
	}
	if err := sim.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "запуск sim-logistics:", err)
		return 1
	}
	defer func() {
		_ = stdin.Close() // штатная остановка: имитатор увидит EOF
		_ = sim.Process.Kill()
		_ = sim.Wait()
	}()
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintln(os.Stderr, "паника в тестах:", r)
			code = 2
		}
	}()

	addr, err := readListenAddr(stdout, time.Minute)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sim-logistics не сообщил адрес:", err)
		return 1
	}
	simURL = "http://" + addr
	if err := waitHealthy(simURL+"/healthz", time.Minute); err != nil {
		fmt.Fprintln(os.Stderr, "sim-logistics не поднялся:", err)
		return 1
	}
	return m.Run()
}

// readListenAddr ждёт строку «listening on <адрес>» в stdout имитатора; остальной вывод уходит в stderr теста.
func readListenAddr(stdout io.Reader, timeout time.Duration) (string, error) {
	const prefix = "listening on "
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			if addr, ok := strings.CutPrefix(line, prefix); ok {
				select {
				case found <- strings.TrimSpace(addr):
				default:
				}
				continue
			}
			fmt.Fprintln(os.Stderr, line)
		}
		close(found)
	}()
	select {
	case addr, ok := <-found:
		if !ok || addr == "" {
			return "", errors.New("stdout закрылся без адреса")
		}
		return addr, nil
	case <-time.After(timeout):
		return "", fmt.Errorf("нет адреса за %v", timeout)
	}
}

func waitHealthy(u string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		resp, err := simHTTP.Get(u)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("нет ответа %s за %v: %v", u, timeout, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// --- обвязка ---

type fixture struct {
	t        *testing.T
	pool     *pgxpool.Pool
	store    *postgres.Store
	clock    *clock.Mutable
	gw       *app.Gateway
	commands string
	replies  string
	log      *slog.Logger

	producer messaging.KafkaProducer
	brokers  []string
}

// settings — параметры прототипа в тестах: max-attempts 3, пауза 1 с, перепроверка UNKNOWN 1 мин, аренда 30 с.
func settings() app.Settings {
	return app.Settings{
		Lease:          30 * time.Second,
		CallTimeout:    5 * time.Second,
		Retry:          domain.RetryPolicy{MaxAttempts: 3, Backoff: time.Second, UnknownRecheck: time.Minute},
		ReconcileBatch: 100,
		// Сверщик в тестах не запускается: его шаг вызывает сам тест (ReconcileDue).
		ReconcileInterval: time.Second,
	}
}

func newFixture(t *testing.T) *fixture {
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
	f := &fixture{
		t: t, pool: pool, store: postgres.New(pool), clock: clock.NewMutable(start),
		commands: testinfra.UniqueTopic(contracts.TopicLogisticsCommands),
		replies:  testinfra.UniqueTopic(contracts.TopicLogisticsReplies),
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	f.simPost("/dev/reset", nil, http.StatusNoContent)
	f.gw = f.newGateway()
	return f
}

// newGateway — ещё один экземпляр шлюза над той же базой: своё соединение с C03, свои владельцы аренды.
func (f *fixture) newGateway() *app.Gateway {
	return f.newGatewayWithClock(f.clock)
}

func (f *fixture) newGatewayWithClock(clk clock.Clock) *app.Gateway {
	journal := messaging.NewJournal("logistics-gateway", auditc.ModuleLogistics, clk)
	return app.New(f.pool, f.store, c03.New(simURL, 5*time.Second), kafka.Outbox{Journal: journal, Topic: f.replies},
		clk, f.log, settings())
}

// steppingClock — часы, которые сдвигаются на step при каждом чтении: так между арендой и отправкой
// «проходит» время без реального ожидания.
type steppingClock struct {
	mu   sync.Mutex
	now  time.Time
	step time.Duration
}

func (c *steppingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.now
	c.now = c.now.Add(c.step)
	return t
}

// withKafka запускает consumer шлюза на топике команд и ретранслятор outbox, как в рабочем сервисе.
func (f *fixture) withKafka() {
	f.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	f.brokers = testinfra.Kafka(f.t)
	topics := append(messaging.WithDLT(f.commands), f.replies, contracts.TopicAuditEvents)
	if err := messaging.EnsureTopics(ctx, f.brokers, 1, topics...); err != nil {
		f.t.Fatal(err)
	}
	consumer, err := messaging.NewConsumer(messaging.ConsumerConfig{
		Brokers: f.brokers, Group: "logistics-gateway-" + uuid.NewString(), Topics: []string{f.commands},
	}, kafka.Handler(f.gw), f.log)
	if err != nil {
		f.t.Fatal(err)
	}
	client, err := messaging.NewProducerClient(f.brokers)
	if err != nil {
		f.t.Fatal(err)
	}
	f.producer = messaging.KafkaProducer{Client: client}
	relay := messaging.NewRelay(f.pool, f.producer, messaging.RelayConfig{Interval: 50 * time.Millisecond}, f.log)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = consumer.Run(ctx) }()
	go func() { defer wg.Done(); _ = relay.Run(ctx) }()
	f.t.Cleanup(func() {
		cancel()
		wg.Wait()
		client.Close()
	})
}

func command() logistics.RequestTransport {
	return logistics.RequestTransport{
		ObligationID: uuid.New(), RequestID: uuid.New(), SourceClinicID: 1, TargetClinicID: 2,
		ComponentKind: "RBC", Quantity: 2, TemperatureRegime: logistics.RegimeCold2to6,
	}
}

func request(t *testing.T, c logistics.RequestTransport) domain.Request {
	t.Helper()
	r, err := kafka.RequestFrom(c)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (f *fixture) send(key uuid.UUID, payload contracts.Payload, messageID uuid.UUID) {
	f.t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		f.t.Fatal(err)
	}
	msg := messaging.Message{
		ID: messageID, Type: payload.MessageType(), SchemaVersion: payload.SchemaVersion(), Topic: f.commands,
		Key: key.String(), Payload: body, Producer: "transfer", OccurredAt: f.clock.Now(),
	}
	if err := f.producer.ProduceSync(context.Background(), msg.Record()); err != nil {
		f.t.Fatal(err)
	}
}

// insert — строка REQUESTING, как после приёма команды, но без drive.
func (f *fixture) insert(c logistics.RequestTransport) {
	f.t.Helper()
	err := bspgx.WithTx(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.store.Insert(ctx, tx, request(f.t, c), f.clock.Now())
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

// recordedByCrashedInstance — строка REQUESTING записана, аренду держит экземпляр, упавший до вызова C03.
func (f *fixture) recordedByCrashedInstance(c logistics.RequestTransport) {
	f.t.Helper()
	f.insert(c)
	_, err := f.pool.Exec(context.Background(),
		`UPDATE transport_request SET lease_owner = $1, lease_until = $2 WHERE obligation_id = $3`,
		uuid.New(), f.clock.Now().Add(30*time.Second), c.ObligationID)
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) drive(id uuid.UUID) {
	f.t.Helper()
	if err := f.gw.Drive(context.Background(), id); err != nil {
		f.t.Fatalf("drive: %v", err)
	}
}

func (f *fixture) row(id uuid.UUID) domain.Row {
	f.t.Helper()
	r, ok, err := f.store.Find(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	if !ok {
		f.t.Fatalf("строки заказа %s нет", id)
	}
	return r
}

func (f *fixture) status(id uuid.UUID) domain.Status {
	f.t.Helper()
	r, ok, err := f.store.Find(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	if !ok {
		return ""
	}
	return r.Status
}

// replies — типы ответов по обязательству в outbox, по порядку записи.
func (f *fixture) repliesOf(id uuid.UUID) []string {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT message_type FROM outbox WHERE topic = $1 AND key = $2 ORDER BY id`, f.replies, id.String())
	if err != nil {
		f.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *fixture) lastReply(id uuid.UUID) map[string]any {
	f.t.Helper()
	var body []byte
	err := f.pool.QueryRow(context.Background(),
		`SELECT payload FROM outbox WHERE topic = $1 AND key = $2 ORDER BY id DESC LIMIT 1`, f.replies, id.String()).Scan(&body)
	if err != nil {
		f.t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *fixture) inboxHas(messageID uuid.UUID) bool {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM inbox WHERE message_id = $1`, messageID).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n == 1
}

// awaitReplyType читает logistics.replies отдельной группой и возвращает тип первого ответа с ключом key.
func (f *fixture) awaitReplyType(key uuid.UUID) string {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	found := make(chan string, 1)
	consumer, err := messaging.NewConsumer(messaging.ConsumerConfig{
		Brokers: f.brokers, Group: "test-" + uuid.NewString(), Topics: []string{f.replies},
	}, func(_ context.Context, msg messaging.Message) error {
		if msg.Key == key.String() {
			select {
			case found <- msg.Type:
			default:
			}
		}
		return nil
	}, f.log)
	if err != nil {
		f.t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = consumer.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case t := <-found:
		return t
	case <-ctx.Done():
		f.t.Fatalf("ответа в %s нет", f.replies)
		return ""
	}
}

// eventually ждёт условия: Kafka и ретранслятор асинхронны.
func (f *fixture) eventually(what string, cond func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			f.t.Fatalf("не дождались: %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// --- пульт имитатора C03 ---

type simShipment struct {
	ShipmentRef string `json:"shipmentRef"`
	ClientRef   string `json:"clientRef"`
	Cancelled   bool   `json:"cancelled"`
}

func (f *fixture) simPost(path string, body any, want int) []byte {
	f.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			f.t.Fatal(err)
		}
	}
	resp, err := simHTTP.Post(simURL+path, "application/json", &buf)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		f.t.Fatalf("POST %s: %d %s, ожидали %d", path, resp.StatusCode, out, want)
	}
	return out
}

func (f *fixture) faults(v map[string]any) {
	f.t.Helper()
	f.simPost("/dev/faults", v, http.StatusOK)
}

// shipments — все заказы у C03 по обязательству, включая отменённые.
func (f *fixture) shipments(id uuid.UUID) []simShipment {
	f.t.Helper()
	resp, err := simHTTP.Get(simURL + "/dev/shipments?clientRef=" + url.QueryEscape(domain.ClientRef(id)))
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var list struct {
		Shipments []simShipment `json:"shipments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		f.t.Fatal(err)
	}
	return list.Shipments
}

// submitDirect — заказ у C03 мимо шлюза: так выглядит заказ упавшего экземпляра, не успевшего записать исход.
func (f *fixture) submitDirect(id uuid.UUID) string {
	f.t.Helper()
	body := f.simPost("/shipments", map[string]any{
		"clientRef": domain.ClientRef(id), "sourceClinicId": 1, "targetClinicId": 2, "componentKind": "RBC",
		"containers": 1, "temperatureRegime": logistics.RegimeCold2to6,
	}, http.StatusCreated)
	var s simShipment
	if err := json.Unmarshal(body, &s); err != nil {
		f.t.Fatal(err)
	}
	return s.ShipmentRef
}

func assertReplies(t *testing.T, got []string, want ...string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ответы %v, ожидали %v", got, want)
	}
}

func assertShipments(t *testing.T, got []simShipment, want int) {
	t.Helper()
	if len(got) != want {
		t.Fatalf("заказов у C03 %d, ожидали %d: %+v", len(got), want, got)
	}
}

// --- сценарии ---

func TestCommandFromKafkaGetsOneShipmentAndReplyInKafka(t *testing.T) {
	f := newFixture(t)
	f.withKafka()
	c := command()
	f.send(c.ObligationID, c, uuid.New())

	f.eventually("ACCEPTED", func() bool { return f.status(c.ObligationID) == domain.StatusAccepted })
	assertShipments(t, f.shipments(c.ObligationID), 1)
	if got := f.awaitReplyType(c.ObligationID); got != "TransportAccepted" {
		t.Fatalf("ответ в logistics.replies %q", got)
	}
}

func TestDuplicateDeliveriesOfACommandCreateOneShipment(t *testing.T) {
	f := newFixture(t)
	f.withKafka()
	c := command()
	messageID := uuid.New()
	f.send(c.ObligationID, c, messageID)
	f.send(c.ObligationID, c, messageID)  // повторная доставка Kafka
	f.send(c.ObligationID, c, uuid.New()) // повтор отправки transfer
	// Барьер: команда по другому обязательству с тем же ключом — та же партиция, обработается после предыдущих.
	other := command()
	f.send(c.ObligationID, other, uuid.New())
	f.eventually("барьер ACCEPTED", func() bool { return f.status(other.ObligationID) == domain.StatusAccepted })

	assertShipments(t, f.shipments(c.ObligationID), 1)
	assertReplies(t, f.repliesOf(c.ObligationID), "TransportAccepted")
}

func TestLostResponseIsReconciledInsteadOfResubmitted(t *testing.T) {
	f := newFixture(t)
	f.withKafka()
	f.faults(map[string]any{"loseNextSubmitResponse": true}) // заказ у C03 создан, ответ до шлюза не дошёл
	c := command()
	f.send(c.ObligationID, c, uuid.New())
	f.eventually("первая попытка засчитана", func() bool {
		r, ok, err := f.store.Find(context.Background(), c.ObligationID)
		return err == nil && ok && r.Attempts == 1
	})
	if s := f.status(c.ObligationID); s != domain.StatusRequesting {
		t.Fatalf("после потерянного ответа статус %s, ожидали REQUESTING", s)
	}

	f.clock.Advance(2 * time.Second)
	f.drive(c.ObligationID)

	shipments := f.shipments(c.ObligationID)
	assertShipments(t, shipments, 1)
	if got := f.row(c.ObligationID).ShipmentRef; got != shipments[0].ShipmentRef {
		t.Fatalf("shipment_ref %q, у C03 %q", got, shipments[0].ShipmentRef)
	}
	assertReplies(t, f.repliesOf(c.ObligationID), "TransportAccepted")
}

func TestCrashAfterRecordingButBeforeCallingC03(t *testing.T) {
	f := newFixture(t)
	c := command()
	f.recordedByCrashedInstance(c)

	f.drive(c.ObligationID)
	if got := f.shipments(c.ObligationID); len(got) != 0 {
		t.Fatalf("аренда упавшего экземпляра ещё действует, а заказ создан: %+v", got)
	}

	f.clock.Advance(31 * time.Second)
	n, err := f.gw.ReconcileDue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("сверщик не подобрал строку с истёкшей арендой")
	}

	if s := f.status(c.ObligationID); s != domain.StatusAccepted {
		t.Fatalf("статус %s, ожидали ACCEPTED", s)
	}
	assertShipments(t, f.shipments(c.ObligationID), 1)
	assertReplies(t, f.repliesOf(c.ObligationID), "TransportAccepted")
}

func TestCrashAfterC03AcceptedButBeforeRecordingTheOutcome(t *testing.T) {
	f := newFixture(t)
	c := command()
	shipmentRef := f.submitDirect(c.ObligationID)
	f.recordedByCrashedInstance(c)

	f.clock.Advance(31 * time.Second)
	f.drive(c.ObligationID)

	assertShipments(t, f.shipments(c.ObligationID), 1)
	if got := f.row(c.ObligationID).ShipmentRef; got != shipmentRef {
		t.Fatalf("shipment_ref %q, ожидали заказ упавшего экземпляра %q", got, shipmentRef)
	}
}

func TestTwoGatewayInstancesDrivingTheSameRequestCreateOneShipment(t *testing.T) {
	f := newFixture(t)
	c := command()
	f.insert(c)
	f.faults(map[string]any{"slowMs": 300}) // окно, в котором второй экземпляр успел бы отправить дубль
	instances := []*app.Gateway{f.gw, f.newGateway()}

	const n = 8
	startLine := make(chan struct{})
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startLine
			errs <- instances[i%len(instances)].Drive(context.Background(), c.ObligationID)
		}()
	}
	close(startLine)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("drive: %v", err)
		}
	}

	assertShipments(t, f.shipments(c.ObligationID), 1)
	assertReplies(t, f.repliesOf(c.ObligationID), "TransportAccepted")
}

func TestCarrierDownTooLongGivesHonestUnknownAndNoBlindResubmit(t *testing.T) {
	f := newFixture(t)
	c := command()
	f.insert(c)
	f.faults(map[string]any{"failNextCalls": 3})
	for range 3 {
		f.drive(c.ObligationID)
		f.clock.Advance(5 * time.Second)
	}
	if s := f.status(c.ObligationID); s != domain.StatusUnknown {
		t.Fatalf("после трёх неудач статус %s, ожидали UNKNOWN", s)
	}
	assertReplies(t, f.repliesOf(c.ObligationID), "TransportRejected")
	if reason := f.lastReply(c.ObligationID)["reason"]; reason != "OUTCOME_UNKNOWN" {
		t.Fatalf("причина %v, ожидали OUTCOME_UNKNOWN", reason)
	}

	f.drive(c.ObligationID) // C03 ожила: сверка не нашла заказ

	if s := f.status(c.ObligationID); s != domain.StatusFailed {
		t.Fatalf("transfer уже сказали «не знаем», второй заказ не шлём: статус %s, ожидали FAILED", s)
	}
	assertShipments(t, f.shipments(c.ObligationID), 0)
}

func TestUnknownOutcomeThatActuallyReachedCarrierEndsAccepted(t *testing.T) {
	f := newFixture(t)
	c := command()
	f.insert(c)
	f.faults(map[string]any{"loseNextSubmitResponse": true})
	f.drive(c.ObligationID) // попытка 1: заказ создан, ответ потерян
	f.faults(map[string]any{"failNextCalls": 2})
	f.drive(c.ObligationID) // попытка 2: сверка не прошла
	f.drive(c.ObligationID) // попытка 3 → UNKNOWN
	if s := f.status(c.ObligationID); s != domain.StatusUnknown {
		t.Fatalf("статус %s, ожидали UNKNOWN", s)
	}

	f.drive(c.ObligationID)

	if s := f.status(c.ObligationID); s != domain.StatusAccepted {
		t.Fatalf("статус %s, ожидали запоздалый ACCEPTED", s)
	}
	assertShipments(t, f.shipments(c.ObligationID), 1)
	assertReplies(t, f.repliesOf(c.ObligationID), "TransportRejected", "TransportAccepted")
}

func TestCancelAfterAcceptanceCancelsAtCarrier(t *testing.T) {
	f := newFixture(t)
	f.withKafka()
	c := command()
	f.send(c.ObligationID, c, uuid.New())
	f.eventually("ACCEPTED", func() bool { return f.status(c.ObligationID) == domain.StatusAccepted })

	f.send(c.ObligationID, logistics.CancelTransport{ObligationID: c.ObligationID, Reason: logistics.CancelUnitWithdrawn}, uuid.New())
	f.eventually("CANCELLED", func() bool { return f.status(c.ObligationID) == domain.StatusCancelled })

	shipments := f.shipments(c.ObligationID)
	assertShipments(t, shipments, 1)
	if !shipments[0].Cancelled {
		t.Fatalf("заказ у C03 не снят: %+v", shipments[0])
	}
	assertReplies(t, f.repliesOf(c.ObligationID), "TransportAccepted", "TransportCancelled")
}

func TestCancelOvertakingTheRequestPreventsTheShipment(t *testing.T) {
	f := newFixture(t)
	f.withKafka()
	c := command()
	f.send(c.ObligationID, logistics.CancelTransport{ObligationID: c.ObligationID, Reason: logistics.CancelUnitWithdrawn}, uuid.New())
	requestID := uuid.New()
	f.send(c.ObligationID, c, requestID)
	f.eventually("поздняя команда принята", func() bool { return f.inboxHas(requestID) })

	if s := f.status(c.ObligationID); s != domain.StatusCancelled {
		t.Fatalf("статус %s, ожидали надгробие CANCELLED", s)
	}
	assertShipments(t, f.shipments(c.ObligationID), 0)
	assertReplies(t, f.repliesOf(c.ObligationID), "TransportCancelled")
}

func TestCarrierRefusalIsReported(t *testing.T) {
	f := newFixture(t)
	f.withKafka()
	f.faults(map[string]any{"refuseNext": "нет свободной машины с термоконтейнером"})
	c := command()
	f.send(c.ObligationID, c, uuid.New())

	f.eventually("REJECTED", func() bool { return f.status(c.ObligationID) == domain.StatusRejected })
	if reason := f.lastReply(c.ObligationID)["reason"]; reason != "REFUSED" {
		t.Fatalf("причина %v, ожидали REFUSED", reason)
	}
}

// --- сценарии сверх прототипа ---

func cancelMessage(t *testing.T, id uuid.UUID) messaging.Message {
	t.Helper()
	body, err := json.Marshal(logistics.CancelTransport{ObligationID: id, Reason: logistics.CancelUnitWithdrawn})
	if err != nil {
		t.Fatal(err)
	}
	return messaging.Message{ID: uuid.New(), Type: "CancelTransport", SchemaVersion: 1, Payload: body, Producer: "transfer"}
}

// Шлюз отвечает на каждую отмену, и когда заказа не было; повтор доставки той же отмены ответа не множит.
func TestEveryCancelIsAnsweredOnceEvenWithoutShipment(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := uuid.New()
	first := cancelMessage(t, id)
	for _, msg := range []messaging.Message{first, first, cancelMessage(t, id)} {
		if err := f.gw.AcceptCancel(ctx, msg, id); err != nil {
			t.Fatal(err)
		}
	}
	assertReplies(t, f.repliesOf(id), "TransportCancelled", "TransportCancelled")
	if s := f.status(id); s != domain.StatusCancelled {
		t.Fatalf("статус %s, ожидали надгробие CANCELLED", s)
	}
}

// Отмена пришла, пока шёл заказ, а перевозчик отказал: transfer получает и отказ, и ответ на отмену.
func TestCancelDuringRefusedSubmitIsAnswered(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := command()
	f.insert(c)
	f.faults(map[string]any{"refuseNext": "нет свободной машины", "slowMs": 1000})
	done := make(chan error, 1)
	go func() { done <- f.gw.Drive(ctx, c.ObligationID) }()
	f.eventually("аренда взята", func() bool {
		var leased bool
		err := f.pool.QueryRow(ctx, `SELECT lease_owner IS NOT NULL FROM transport_request WHERE obligation_id = $1`,
			c.ObligationID).Scan(&leased)
		return err == nil && leased
	})
	if err := f.gw.AcceptCancel(ctx, cancelMessage(t, c.ObligationID), c.ObligationID); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("drive: %v", err)
	}

	if s := f.status(c.ObligationID); s != domain.StatusRejected {
		t.Fatalf("статус %s, ожидали REJECTED", s)
	}
	assertShipments(t, f.shipments(c.ObligationID), 0)
	assertReplies(t, f.repliesOf(c.ObligationID), "TransportRejected", "TransportCancelled")
}

// Сверка съела аренду (часы сдвигаются на 26 с при каждом чтении, до конца аренды меньше таймаута C03):
// заказ не отправляется, ход отпущен без засчитанной попытки, и следующий drive доводит заказ до конца.
func TestSubmitIsSkippedWhenLeaseIsRunningOut(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := command()
	f.insert(c)
	slow := f.newGatewayWithClock(&steppingClock{now: start, step: 26 * time.Second})

	if err := slow.Drive(ctx, c.ObligationID); err != nil {
		t.Fatalf("drive: %v", err)
	}
	assertShipments(t, f.shipments(c.ObligationID), 0)
	r := f.row(c.ObligationID)
	if r.Status != domain.StatusRequesting || r.Attempts != 0 {
		t.Fatalf("строка после отпущенного хода: %+v", r)
	}
	var leased bool
	if err := f.pool.QueryRow(ctx, `SELECT lease_owner IS NOT NULL FROM transport_request WHERE obligation_id = $1`,
		c.ObligationID).Scan(&leased); err != nil {
		t.Fatal(err)
	}
	if leased {
		t.Fatal("аренда не отпущена")
	}

	f.drive(c.ObligationID)
	assertShipments(t, f.shipments(c.ObligationID), 1)
	assertReplies(t, f.repliesOf(c.ObligationID), "TransportAccepted")
}

// Приём команды и запрос отмены попадают в аудит теми же транзакциями; повтор команды второй записи не даёт.
func TestAcceptedCommandsAreAudited(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := command()
	body, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 { // повтор отправки transfer с новым message-id
		msg := messaging.Message{ID: uuid.New(), Type: "RequestTransport", SchemaVersion: 1, Payload: body, Producer: "transfer"}
		if err := f.gw.AcceptRequest(ctx, msg, request(t, c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.gw.AcceptCancel(ctx, cancelMessage(t, c.ObligationID), c.ObligationID); err != nil {
		t.Fatal(err)
	}
	rows, err := f.pool.Query(ctx, `SELECT action FROM audit_local WHERE object_type = 'obligation' AND object_id = $1 ORDER BY id`,
		c.ObligationID.String())
	if err != nil {
		t.Fatal(err)
	}
	actions, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	// Приём, исход заказа (ACCEPTED), запрос отмены, исход отмены (CANCELLED).
	want := []string{"REQUEST_ACCEPTED", "TRANSPORT_ACCEPTED", "CANCEL_REQUESTED", "TRANSPORT_CANCELLED"}
	if fmt.Sprint(actions) != fmt.Sprint(want) {
		t.Fatalf("аудит %v, ожидали %v", actions, want)
	}
}

// Исход пишется только при ожидаемом статусе: аренда наша, но строку успели перевести — запись отбрасывается.
func TestOutcomeIsNotWrittenOverChangedStatus(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := command()
	f.insert(c)
	owner := uuid.New()
	now := f.clock.Now()
	if _, ok, err := f.store.Claim(ctx, c.ObligationID, owner, now, now.Add(30*time.Second)); err != nil || !ok {
		t.Fatalf("аренда: %v, %v", ok, err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE transport_request SET status = 'UNKNOWN' WHERE obligation_id = $1`, c.ObligationID); err != nil {
		t.Fatal(err)
	}
	err := bspgx.WithTx(ctx, f.pool, func(ctx context.Context, tx pgx.Tx) error {
		finished, _, err := f.store.Finish(ctx, tx, c.ObligationID, owner, domain.StatusRequesting, domain.StatusAccepted,
			"C03-1", now)
		if err != nil {
			return err
		}
		if finished {
			t.Error("исход записан поверх изменившегося статуса")
		}
		failed, err := f.store.FailAttempt(ctx, tx, c.ObligationID, owner, domain.StatusRequesting, domain.StatusRequesting,
			"C03 недоступна", now, now)
		if err != nil {
			return err
		}
		if failed {
			t.Error("неудачная попытка записана поверх изменившегося статуса")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if s := f.status(c.ObligationID); s != domain.StatusUnknown {
		t.Fatalf("статус %s, ожидали нетронутый UNKNOWN", s)
	}
}
