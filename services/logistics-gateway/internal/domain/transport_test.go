package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/domain"
)

var now = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

var policy = domain.RetryPolicy{MaxAttempts: 3, Backoff: time.Second, UnknownRecheck: time.Minute}

func request() domain.Request {
	return domain.Request{
		ObligationID: uuid.New(), RequestID: uuid.New(), SourceClinicID: 1, TargetClinicID: 2,
		ComponentKindCode: "RBC", Quantity: 2, TemperatureRegime: "COLD_2_6",
	}
}

func row(status domain.Status) domain.Row {
	return domain.Row{Request: request(), Status: status}
}

func TestClientRefIsLowercaseUUIDWithPrefix(t *testing.T) {
	id := uuid.MustParse("0192F1A2-3B4C-7D5E-8F60-718293A4B5C6")
	if got := domain.ClientRef(id); got != "o07-0192f1a2-3b4c-7d5e-8f60-718293a4b5c6" {
		t.Fatalf("clientRef %q", got)
	}
}

func TestRequestValidation(t *testing.T) {
	if err := request().Validate(); err != nil {
		t.Fatalf("корректная команда отвергнута: %v", err)
	}
	cases := map[string]func(r *domain.Request){
		"нет обязательства":     func(r *domain.Request) { r.ObligationID = uuid.Nil },
		"нет request_id":        func(r *domain.Request) { r.RequestID = uuid.Nil },
		"нет источника":         func(r *domain.Request) { r.SourceClinicID = 0 },
		"нет получателя":        func(r *domain.Request) { r.TargetClinicID = -1 },
		"нет вида":              func(r *domain.Request) { r.ComponentKindCode = "" },
		"источник = получатель": func(r *domain.Request) { r.TargetClinicID = r.SourceClinicID },
		"ноль контейнеров":      func(r *domain.Request) { r.Quantity = 0 },
		"нет режима перевозки":  func(r *domain.Request) { r.TemperatureRegime = "" },
	}
	for name, mutate := range cases {
		r := request()
		mutate(&r)
		if err := r.Validate(); !errors.Is(err, domain.ErrInvalidRequest) {
			t.Errorf("%s: ожидали ErrInvalidRequest, получили %v", name, err)
		}
	}
}

func TestNothingToCancelOnlyWithoutShipment(t *testing.T) {
	for s, want := range map[domain.Status]bool{
		domain.StatusRejected: true, domain.StatusFailed: true, domain.StatusCancelled: true,
		domain.StatusRequesting: false, domain.StatusAccepted: false, domain.StatusUnknown: false,
	} {
		if got := domain.NothingToCancel(s); got != want {
			t.Errorf("%s: %v, ожидали %v", s, got, want)
		}
	}
}

func TestBeginReconcilesBeforeAnything(t *testing.T) {
	for _, s := range []domain.Status{domain.StatusRequesting, domain.StatusUnknown} {
		if d := row(s).Begin(); d.Step != domain.StepReconcile {
			t.Errorf("%s: первый шаг %v, ожидали сверку", s, d.Step)
		}
	}
	accepted := row(domain.StatusAccepted)
	accepted.CancelRequested = true
	d := accepted.Begin()
	if d.Step != domain.StepCancelAtCarrier || d.Outcome.Status != domain.StatusCancelled ||
		d.Outcome.Reply == nil || d.Outcome.Reply.Kind != domain.ReplyCancelled {
		t.Fatalf("принятый заказ с отменой: %+v", d)
	}
}

func TestFoundShipmentIsAcceptedInsteadOfResubmitted(t *testing.T) {
	for _, s := range []domain.Status{domain.StatusRequesting, domain.StatusUnknown} {
		r := row(s)
		d := r.AfterReconcile("C03-7")
		if d.Step != domain.StepDone || d.Outcome.Status != domain.StatusAccepted || d.Outcome.ShipmentRef != "C03-7" {
			t.Fatalf("%s: найденный заказ: %+v", s, d)
		}
		if rep := d.Outcome.Reply; rep == nil || rep.Kind != domain.ReplyAccepted || rep.ShipmentRef != "C03-7" ||
			rep.ObligationID != r.ObligationID {
			t.Fatalf("%s: ответ %+v", s, d.Outcome.Reply)
		}
	}
}

func TestFoundShipmentWithCancelIsCancelledAtCarrier(t *testing.T) {
	r := row(domain.StatusRequesting)
	r.CancelRequested = true
	if d := r.AfterReconcile("C03-1"); d.Step != domain.StepCancelAtCarrier || d.Outcome.Status != domain.StatusCancelled {
		t.Fatalf("отмена при найденном заказе: %+v", d)
	}
}

func TestNoShipmentWithCancelEndsCancelledWithoutCarrier(t *testing.T) {
	r := row(domain.StatusRequesting)
	r.CancelRequested = true
	d := r.AfterReconcile("")
	if d.Step != domain.StepDone || d.Outcome.Status != domain.StatusCancelled || d.Outcome.Reply.Kind != domain.ReplyCancelled {
		t.Fatalf("отмена без заказа: %+v", d)
	}
}

func TestUnknownWithoutShipmentFailsWithoutResubmit(t *testing.T) {
	d := row(domain.StatusUnknown).AfterReconcile("")
	if d.Step != domain.StepDone || d.Outcome.Status != domain.StatusFailed || d.Outcome.Reply != nil {
		t.Fatalf("UNKNOWN без заказа: %+v; ожидали FAILED без отправки и без ответа", d)
	}
}

func TestRequestingWithoutShipmentSubmits(t *testing.T) {
	if d := row(domain.StatusRequesting).AfterReconcile(""); d.Step != domain.StepSubmit {
		t.Fatalf("REQUESTING без заказа: %+v", d)
	}
}

func TestBackoffDoublesUpToSixtyFourBases(t *testing.T) {
	want := []time.Duration{1, 2, 4, 8, 16, 32, 64, 64, 64}
	for i, w := range want {
		if got := policy.BackoffFor(i + 1); got != w*time.Second {
			t.Errorf("попытка %d: пауза %v, ожидали %v", i+1, got, w*time.Second)
		}
	}
	if got := policy.BackoffFor(0); got != time.Second {
		t.Errorf("попытка 0: пауза %v", got)
	}
}

func TestCarrierDownBelowMaxAttemptsRetriesWithBackoff(t *testing.T) {
	r := row(domain.StatusRequesting)
	r.Attempts = 1
	f := policy.OnCarrierUnavailable(r, now)
	if f.Status != domain.StatusRequesting || f.Reply != nil || !f.NextAttemptAt.Equal(now.Add(2*time.Second)) {
		t.Fatalf("вторая неудача: %+v", f)
	}
}

func TestCarrierDownMaxAttemptsGivesHonestUnknown(t *testing.T) {
	r := row(domain.StatusRequesting)
	r.Attempts = 2
	f := policy.OnCarrierUnavailable(r, now)
	if f.Status != domain.StatusUnknown || !f.NextAttemptAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("третья неудача: %+v", f)
	}
	if f.Reply == nil || f.Reply.Kind != domain.ReplyRejected || f.Reply.Reason != domain.RejectOutcomeUnknown ||
		f.Reply.ObligationID != r.ObligationID {
		t.Fatalf("ответ при UNKNOWN: %+v", f.Reply)
	}
}

func TestUnknownIsRecheckedWithoutSecondReply(t *testing.T) {
	r := row(domain.StatusUnknown)
	r.Attempts = 7
	f := policy.OnCarrierUnavailable(r, now)
	if f.Status != domain.StatusUnknown || f.Reply != nil || !f.NextAttemptAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("перепроверка UNKNOWN: %+v", f)
	}
}

func TestCancelRequestedNeverBecomesUnknown(t *testing.T) {
	r := row(domain.StatusRequesting)
	r.Attempts = 10
	r.CancelRequested = true
	if f := policy.OnCarrierUnavailable(r, now); f.Status != domain.StatusRequesting || f.Reply != nil {
		t.Fatalf("отменённый заказ при недоступной C03: %+v", f)
	}
	accepted := row(domain.StatusAccepted)
	accepted.CancelRequested = true
	accepted.Attempts = 10
	if f := policy.OnCarrierUnavailable(accepted, now); f.Status != domain.StatusAccepted || f.Reply != nil {
		t.Fatalf("снятие принятого заказа при недоступной C03: %+v", f)
	}
}
