//go:build integration

package app_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testinfra"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/adapters/smtpmail"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/adapters/smtpmail/smtptest"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/domain"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/letters"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/recipients"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/migrations"
)

const (
	operatorC001 = "transfusion.c001@demo.blood-supply.local"
	dutyShift    = "duty-shift@demo.blood-supply.local"
)

var (
	start        = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	obligationID = uuid.MustParse("0192f3a1-7b2c-7d4e-8f90-300000000001")
	requestID    = uuid.MustParse("0192f3a1-7b2c-7d4e-8f90-200000000001")
)

type env struct {
	svc   *app.Service
	pool  *pgxpool.Pool
	clock *clock.Mutable
	smtp  *smtptest.Server
}

func setup(t *testing.T, dir app.Directory) env {
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
	ref := refdata.MustLoad()
	if dir == nil {
		d, err := recipients.Default(ref.Clinics)
		if err != nil {
			t.Fatal(err)
		}
		dir = d
	}
	clk := clock.NewMutable(start)
	srv := smtptest.Start(t)
	mailer, err := smtpmail.New(smtpmail.Config{Addr: srv.Addr(), From: "no-reply@demo.blood-supply.local", Timeout: 3 * time.Second}, clk)
	if err != nil {
		t.Fatal(err)
	}
	r, err := letters.New(ref, "http://localhost:8080")
	if err != nil {
		t.Fatal(err)
	}
	svc := app.New(app.Deps{
		Pool: pool, Store: postgres.New(pool), Directory: dir, Letters: r, Mailer: mailer,
		Journal: messaging.NewJournal("notification", "notification", clk), Clock: clk,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, app.DefaultConfig())
	return env{svc: svc, pool: pool, clock: clk, smtp: srv}
}

func message(t *testing.T, payload contracts.Payload, extra map[string]any) messaging.Message {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if extra != nil {
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatal(err)
		}
		for k, v := range extra {
			m[k] = v
		}
		if body, err = json.Marshal(m); err != nil {
			t.Fatal(err)
		}
	}
	return messaging.Message{
		ID: uuid.New(), Type: payload.MessageType(), SchemaVersion: payload.SchemaVersion(),
		Topic: contracts.TopicTransferEvents, Key: obligationID.String(), Payload: body,
		Producer: "transfer", OccurredAt: start,
	}
}

func opened(origin string) transfer.ObligationOpened {
	return transfer.ObligationOpened{
		ObligationID: obligationID, RequestID: requestID, Origin: origin,
		SourceClinicID: 1, TargetClinicID: 2, ComponentKind: "RBC", RequestedQty: 3,
		Deadline: start.Add(2 * time.Hour),
	}
}

func handle(t *testing.T, e env, msgs ...messaging.Message) {
	t.Helper()
	for _, m := range msgs {
		if err := e.svc.HandleMessage(context.Background(), m); err != nil {
			t.Fatalf("%s: %v", m.Type, err)
		}
	}
}

func sendDue(t *testing.T, e env) int {
	t.Helper()
	n, err := e.svc.SendDue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func list(t *testing.T, e env, status *domain.Status) []domain.Notification {
	t.Helper()
	items, err := e.svc.List(context.Background(), app.ListFilter{Status: status})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func only(t *testing.T, e env) domain.Notification {
	t.Helper()
	items := list(t, e, nil)
	if len(items) != 1 {
		t.Fatalf("записей в журнале писем %d, ожидалась 1", len(items))
	}
	return items[0]
}

func count(t *testing.T, e env, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func status(s domain.Status) *domain.Status { return &s }

// FR044: выбор врача → письмо оператору клиники-источника уходит на SMTP, запись SENT, действия в аудите.
func TestDoctorChoiceLetterReachesSourceOperator(t *testing.T) {
	e := setup(t, nil)
	handle(t, e, message(t, opened(transfer.OriginDoctorChoice), nil))

	n := only(t, e)
	if n.Status != domain.StatusPending || n.Kind != domain.KindUrgentTask || n.Email != operatorC001 {
		t.Fatalf("после события: %s %s → %q", n.Status, n.Kind, n.Email)
	}
	if got := sendDue(t, e); got != 1 {
		t.Fatalf("отправлено %d, ожидалось 1", got)
	}
	mails := e.smtp.Messages()
	if len(mails) != 1 || len(mails[0].To) != 1 || mails[0].To[0] != operatorC001 {
		t.Fatalf("письма на SMTP: %+v", mails)
	}
	l, err := mails[0].Parse()
	if err != nil {
		t.Fatal(err)
	}
	if l.Subject != "[демо] Срочное задание на передачу № 0192f3a1" {
		t.Fatalf("тема %q", l.Subject)
	}
	if !strings.Contains(l.Text, obligationID.String()) || !strings.Contains(l.HTML, "/tasks/"+obligationID.String()) {
		t.Fatal("в письме нет номера задания или ссылки на карточку")
	}
	n = only(t, e)
	if n.Status != domain.StatusSent || n.SentAt == nil || !n.SentAt.Equal(start) || n.Attempts != 1 {
		t.Fatalf("после отправки: %s, sent_at %v, попыток %d", n.Status, n.SentAt, n.Attempts)
	}
	for _, action := range []string{"NOTIFICATION_QUEUED", "NOTIFICATION_SENT"} {
		if count(t, e, `SELECT count(*) FROM audit_local WHERE action = $1 AND object_id = $2`, action, obligationID.String()) != 1 {
			t.Errorf("нет записи аудита %s", action)
		}
	}
	if sendDue(t, e) != 0 || len(e.smtp.Messages()) != 1 {
		t.Fatal("отправленное письмо ушло повторно")
	}
}

func TestPlannedDoctorChoiceDoesNotQueueUrgentLetter(t *testing.T) {
	e := setup(t, nil)
	event := opened(transfer.OriginDoctorChoice)
	event.Urgency = transfer.UrgencyPlanned
	handle(t, e, message(t, event, nil))
	if items := list(t, e, nil); len(items) != 0 {
		t.Fatalf("плановое задание вызвало срочное письмо: %+v", items)
	}
}

// Задание из плана пополнения письма не порождает, но сведения о задании сохраняются для эскалаций.
func TestPlanProposalOpensNoLetter(t *testing.T) {
	e := setup(t, nil)
	handle(t, e, message(t, opened(transfer.OriginPlanProposal), nil))
	if items := list(t, e, nil); len(items) != 0 {
		t.Fatalf("письмо по заданию плана: %+v", items)
	}
	if count(t, e, `SELECT count(*) FROM obligation_projection WHERE obligation_id = $1`, obligationID) != 1 {
		t.Fatal("сведения о задании не сохранены в проекции")
	}
}

// Kafka доставляет «хотя бы один раз»: три доставки события дают одно письмо (inbox).
func TestDuplicateDeliveryProducesOneLetter(t *testing.T) {
	e := setup(t, nil)
	msg := message(t, opened(transfer.OriginDoctorChoice), nil)
	handle(t, e, msg, msg, msg)
	only(t, e)
	if got := sendDue(t, e); got != 1 {
		t.Fatalf("отправлено %d, ожидалось 1", got)
	}
	if len(e.smtp.Messages()) != 1 {
		t.Fatalf("писем на SMTP %d, ожидалось 1", len(e.smtp.Messages()))
	}
}

// SMTP недоступен: запись остаётся PENDING, попытка откладывается по паузе; задание в transfer не трогается —
// сервис не пишет ничего, кроме аудита. Когда SMTP поднялся и пауза прошла, письмо уходит.
func TestSMTPDownKeepsPendingAndRetriesLater(t *testing.T) {
	e := setup(t, nil)
	e.smtp.SetDown(true)
	handle(t, e, message(t, opened(transfer.OriginDoctorChoice), nil))

	if got := sendDue(t, e); got != 0 {
		t.Fatalf("при недоступном SMTP отправлено %d", got)
	}
	n := only(t, e)
	pause := app.DefaultConfig().Retry.Delay(1)
	if n.Status != domain.StatusPending || n.Attempts != 1 || !n.NextAttemptAt.Equal(start.Add(pause)) {
		t.Fatalf("после сбоя: %s, попыток %d, следующая %v (ожидалась %v)", n.Status, n.Attempts, n.NextAttemptAt, start.Add(pause))
	}
	if n.LastError == "" || strings.Contains(n.LastError, "Срочное задание") || strings.Contains(n.LastError, obligationID.String()) {
		t.Fatalf("last_error пуст или содержит текст письма: %q", n.LastError)
	}

	// Пауза не прошла — попытки нет, даже если SMTP уже поднялся.
	e.smtp.SetDown(false)
	if got := sendDue(t, e); got != 0 || only(t, e).Attempts != 1 {
		t.Fatal("повтор раньше паузы")
	}

	e.clock.Advance(pause)
	if got := sendDue(t, e); got != 1 {
		t.Fatalf("после паузы отправлено %d, ожидалось 1", got)
	}
	n = only(t, e)
	if n.Status != domain.StatusSent || n.Attempts != 2 || n.LastError != "" {
		t.Fatalf("после повтора: %s, попыток %d, last_error %q", n.Status, n.Attempts, n.LastError)
	}
	if other := count(t, e, `SELECT count(*) FROM outbox WHERE topic <> $1`, contracts.TopicAuditEvents); other != 0 {
		t.Fatalf("сервис уведомлений отправил %d сообщений помимо аудита: задание меняет только transfer", other)
	}
}

// Эскалация и нарушение обязательства уходят дежурному смены, сведения о задании — из проекции ObligationOpened.
func TestEscalationAndBrokenGoToDutyOfficer(t *testing.T) {
	e := setup(t, nil)
	handle(t, e,
		message(t, opened(transfer.OriginPlanProposal), nil),
		message(t, transfer.EscalationRaised{ObligationID: obligationID, RequestID: requestID, Reason: transfer.EscalationExpiredNoFallback, At: start}, nil),
		message(t, transfer.TransferBroken{ObligationID: obligationID, RequestID: requestID, UnitID: uuid.New(), Reason: transfer.BrokenQuarantine}, nil),
	)
	items := list(t, e, status(domain.StatusPending))
	if len(items) != 2 {
		t.Fatalf("писем дежурному %d, ожидалось 2", len(items))
	}
	for _, n := range items {
		if n.To.Role != domain.RoleDuty || n.Email != dutyShift {
			t.Fatalf("%s: адресат %s %q", n.Kind, n.To.Role, n.Email)
		}
		if n.Facts.SourceClinicID == nil || *n.Facts.SourceClinicID != 1 || n.Facts.Reason == "" {
			t.Fatalf("%s: сведения из проекции не перенесены: %+v", n.Kind, n.Facts)
		}
	}
	if got := sendDue(t, e); got != 2 {
		t.Fatalf("отправлено %d, ожидалось 2", got)
	}
	subjects := map[string]bool{}
	for _, m := range e.smtp.Messages() {
		l, err := m.Parse()
		if err != nil {
			t.Fatal(err)
		}
		subjects[l.Subject] = true
	}
	for _, want := range []string{"[демо] Эскалация по заданию № 0192f3a1", "[демо] Нарушено обязательство на передачу № 0192f3a1"} {
		if !subjects[want] {
			t.Errorf("нет письма %q; получены %v", want, subjects)
		}
	}
}

// Что бы ни лежало в событии сверх контракта, в письмо попадают только разрешённые сведения (FR044, NFR016).
func TestLetterCarriesNoPatientDataEvenIfEventDoes(t *testing.T) {
	e := setup(t, nil)
	secrets := map[string]any{
		"phenotype":       "C+c-E+e-K-",
		"patient_case_no": "КС-2026-000123",
		"full_name":       "Иванов Иван Иванович",
		"spec":            map[string]any{"phenotype": "Cw+", "blood_group": "O-"},
	}
	handle(t, e, message(t, opened(transfer.OriginDoctorChoice), secrets))
	if sendDue(t, e) != 1 {
		t.Fatal("письмо не ушло")
	}
	m := e.smtp.Messages()[0]
	l, err := m.Parse()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"C+c-E+e-K-", "КС-2026-000123", "Иванов", "Cw+", "phenotype", "patient_case_no"} {
		for part, body := range map[string]string{"тема": l.Subject, "text": l.Text, "html": l.HTML, "сырое": string(m.Raw)} {
			if strings.Contains(body, s) {
				t.Errorf("%s письма содержит %q", part, s)
			}
		}
	}
}

// Топик общий: чужие события игнорируются без ошибки и без записи в inbox.
func TestUnrelatedEventsAreIgnored(t *testing.T) {
	e := setup(t, nil)
	handle(t, e,
		message(t, transfer.TransferConfirmed{ObligationID: obligationID, RequestID: requestID, Units: []uuid.UUID{uuid.New()}, Quantity: 1, Operator: "operator-c001"}, nil),
		messaging.Message{ID: uuid.New(), Type: "SomethingNew", Topic: contracts.TopicTransferEvents, Payload: []byte(`{}`)},
	)
	if len(list(t, e, nil)) != 0 || count(t, e, `SELECT count(*) FROM inbox`) != 0 {
		t.Fatal("чужое событие оставило след")
	}
}

// Битое тело нашего события — постоянная ошибка (в DLT), а не бесконечные повторы.
func TestMalformedEventIsPermanent(t *testing.T) {
	e := setup(t, nil)
	msg := messaging.Message{ID: uuid.New(), Type: "ObligationOpened", Topic: contracts.TopicTransferEvents, Payload: []byte(`{`)}
	if err := e.svc.HandleMessage(context.Background(), msg); err == nil || !messaging.IsPermanent(err) {
		t.Fatalf("ожидалась постоянная ошибка, получено %v", err)
	}
}

// Адресат отклонён сервером (550) — запись сразу FAILED, без повторов, с записью аудита.
func TestRejectedRecipientFailsAtOnce(t *testing.T) {
	e := setup(t, nil)
	e.smtp.RejectRecipients(true)
	handle(t, e, message(t, opened(transfer.OriginDoctorChoice), nil))
	if sendDue(t, e) != 0 {
		t.Fatal("отклонённое письмо посчитано отправленным")
	}
	n := only(t, e)
	if n.Status != domain.StatusFailed || n.Attempts != 1 || !strings.Contains(n.LastError, "550") {
		t.Fatalf("после отказа 550: %s, попыток %d, %q", n.Status, n.Attempts, n.LastError)
	}
	if count(t, e, `SELECT count(*) FROM audit_local WHERE action = 'NOTIFICATION_FAILED'`) != 1 {
		t.Fatal("нет записи аудита об отказе")
	}
	e.clock.Advance(24 * time.Hour)
	if sendDue(t, e) != 0 || len(e.smtp.Messages()) != 0 {
		t.Fatal("FAILED отправлен повторно")
	}
}

// Клиника без адреса: уведомление не теряется молча — запись FAILED видна ИТ-службе (ADR-0021).
func TestClinicWithoutAddressIsVisibleAsFailed(t *testing.T) {
	dir, err := recipients.Load(strings.NewReader(`{"version":1,"duty":"duty-shift@demo.blood-supply.local","operators":{"2":"c002@demo.blood-supply.local"}}`),
		refdata.MustLoad().Clinics)
	if err != nil {
		t.Fatal(err)
	}
	e := setup(t, dir)
	handle(t, e, message(t, opened(transfer.OriginDoctorChoice), nil))
	failed := list(t, e, status(domain.StatusFailed))
	if len(failed) != 1 || failed[0].Email != "" || failed[0].LastError != domain.ErrNoRecipient {
		t.Fatalf("ожидалась одна запись FAILED без адреса: %+v", failed)
	}
	if sendDue(t, e) != 0 {
		t.Fatal("запись без адреса не отправляется")
	}
}
