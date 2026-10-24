package live

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
)

var start = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func msg(t *testing.T, p contracts.Payload, at time.Time) messaging.Message {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return messaging.Message{ID: uuid.New(), Type: p.MessageType(), SchemaVersion: p.SchemaVersion(),
		Topic: contracts.TopicTransferEvents, Payload: raw, OccurredAt: at}
}

func newFeed(h *Hub) *Feed {
	return NewFeed(h, clock.NewMutable(start), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func opened(id uuid.UUID, source, target int64) *transfer.ObligationOpened {
	return &transfer.ObligationOpened{ObligationID: id, RequestID: uuid.New(), SourceClinicID: source, TargetClinicID: target,
		ComponentKind: "RBC", RequestedQty: 2, Deadline: start.Add(time.Hour)}
}

// Клиники обязательства запоминаются из ObligationOpened и подставляются в следующие события.
func TestFeedPublishesStatusWithClinicsFromOpened(t *testing.T) {
	h := NewHub(8)
	c1 := h.Subscribe(clinic(1))
	f := newFeed(h)
	id := uuid.New()

	if err := f.Handle(context.Background(), msg(t, opened(id, 1, 5), start.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	e, ok := recv(c1, time.Second)
	if !ok || e.Status != "PENDING" || e.SourceClinicID != 1 || e.TargetClinicID != 5 {
		t.Fatalf("ObligationOpened: %+v", e)
	}

	confirmed := &transfer.TransferConfirmed{ObligationID: id, RequestID: uuid.New(), Units: []uuid.UUID{uuid.New()}, Quantity: 1, Operator: "operator-c001"}
	if err := f.Handle(context.Background(), msg(t, confirmed, start.Add(2*time.Second))); err != nil {
		t.Fatal(err)
	}
	e, ok = recv(c1, time.Second)
	if !ok || e.Status != "CONFIRMED" || e.SourceClinicID != 1 {
		t.Fatalf("TransferConfirmed: %+v", e)
	}
	if !strings.Contains(e.HTML, id.String()) || !strings.Contains(e.HTML, "Подтверждено, единицы закреплены") {
		t.Fatalf("фрагмент статуса: %s", e.HTML)
	}
	if strings.Contains(e.HTML, "operator-c001") {
		t.Fatal("во фрагменте только статус")
	}
}

// События до запуска web не показываются (только новые), но клиники из них запоминаются.
func TestFeedSkipsOldEventsButLearnsClinics(t *testing.T) {
	h := NewHub(8)
	c1 := h.Subscribe(clinic(1))
	f := newFeed(h)
	id := uuid.New()
	if err := f.Handle(context.Background(), msg(t, opened(id, 1, 5), start.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, ok := recv(c1, 50*time.Millisecond); ok {
		t.Fatal("старое событие не рассылается")
	}
	if err := f.Handle(context.Background(), msg(t, &transfer.TransportRequested{ObligationID: id, ShipmentRef: "S-1"}, start.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	if e, ok := recv(c1, time.Second); !ok || e.Status != "TRANSPORT_REQUESTED" || !strings.Contains(e.HTML, "Перевозка запрошена") {
		t.Fatalf("новое событие по старому обязательству: %+v", e)
	}
}

// msgWithClinics — событие с необязательными полями клиник (расширение контракта transfer.events).
func msgWithClinics(t *testing.T, p contracts.Payload, source, target int64, at time.Time) messaging.Message {
	t.Helper()
	m := msg(t, p, at)
	var fields map[string]any
	if err := json.Unmarshal(m.Payload, &fields); err != nil {
		t.Fatal(err)
	}
	fields["source_clinic_id"], fields["target_clinic_id"] = source, target
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	m.Payload = raw
	return m
}

// Клиники из полей события: ObligationOpened этому экземпляру не нужен.
func TestFeedUsesClinicsFromEventFields(t *testing.T) {
	h := NewHub(8)
	c1, c7 := h.Subscribe(clinic(1)), h.Subscribe(clinic(7))
	f := newFeed(h)
	id := uuid.New()
	m := msgWithClinics(t, &transfer.TransferConfirmed{ObligationID: id, Quantity: 1}, 1, 5, start.Add(time.Second))
	if err := f.Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if e, ok := recv(c1, time.Second); !ok || e.SourceClinicID != 1 || e.TargetClinicID != 5 || e.Status != "CONFIRMED" {
		t.Fatalf("клиника из полей события: %+v", e)
	}
	// Клиники события важнее индекса и пополняют его для следующих событий без полей.
	f.remember(id, 7, 8)
	m = msgWithClinics(t, &transfer.TransportRequested{ObligationID: id, ShipmentRef: "S-1"}, 1, 5, start.Add(2*time.Second))
	if err := f.Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if _, ok := recv(c1, time.Second); !ok {
		t.Fatal("поля события важнее индекса")
	}
	if _, ok := recv(c7, 50*time.Millisecond); ok {
		t.Fatal("устаревший индекс не используется, если клиники есть в событии")
	}
	if err := f.Handle(context.Background(), msg(t, &transfer.TransferBroken{ObligationID: id, UnitID: uuid.New()}, start.Add(3*time.Second))); err != nil {
		t.Fatal(err)
	}
	if e, ok := recv(c1, time.Second); !ok || e.Status != "BROKEN" {
		t.Fatalf("событие без полей — по индексу, пополненному из событий: %+v", e)
	}
}

func TestFeedStatusMapping(t *testing.T) {
	h := NewHub(16)
	all := h.Subscribe(nil)
	f := newFeed(h)
	id := uuid.New()
	at := start.Add(time.Second)
	cases := []struct {
		p    contracts.Payload
		want string
	}{
		{&transfer.TransferRejected{ObligationID: id, Reason: transfer.RejectSourceRefused}, "REJECTED"},
		{&transfer.TransferExpired{ObligationID: id}, "EXPIRED"},
		{&transfer.TransferCancelled{ObligationID: id, Reason: transfer.CancelRequestCancelled}, "CANCELLED"},
		{&transfer.TransferBroken{ObligationID: id, UnitID: uuid.New(), Reason: "EXPIRED"}, "BROKEN"},
	}
	for _, c := range cases {
		if err := f.Handle(context.Background(), msg(t, c.p, at)); err != nil {
			t.Fatal(err)
		}
		if e, ok := recv(all, time.Second); !ok || e.Status != c.want {
			t.Fatalf("%s: получен %+v", c.p.MessageType(), e)
		}
	}
	// События без смены статуса O07 не рассылаются.
	if err := f.Handle(context.Background(), msg(t, &transfer.TransferUnitReplaced{ObligationID: id, OldUnitID: uuid.New(), NewUnitID: uuid.New()}, at)); err != nil {
		t.Fatal(err)
	}
	if _, ok := recv(all, 50*time.Millisecond); ok {
		t.Fatal("замена единицы статус не меняет")
	}
}

// Показ — не бизнес-обработка: ошибка разбора или неизвестный тип не ведут к повторам и DLT.
func TestFeedNeverFails(t *testing.T) {
	f := newFeed(NewHub(1))
	bad := messaging.Message{ID: uuid.New(), Type: "TransferConfirmed", Payload: []byte("{"), OccurredAt: start.Add(time.Second)}
	if err := f.Handle(context.Background(), bad); err != nil {
		t.Fatalf("ошибка разбора не возвращается: %v", err)
	}
	unknown := messaging.Message{ID: uuid.New(), Type: "SomethingNew", Payload: []byte("{}"), OccurredAt: start.Add(time.Second)}
	if err := f.Handle(context.Background(), unknown); err != nil {
		t.Fatal(err)
	}
}

func TestFeedIndexIsBounded(t *testing.T) {
	f := newFeed(NewHub(1))
	f.capacity = 3
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	for _, id := range ids {
		f.remember(id, 1, 2)
	}
	if _, ok := f.clinics(ids[0]); ok {
		t.Fatal("самое старое обязательство вытеснено")
	}
	if s, ok := f.clinics(ids[3]); !ok || s.source != 1 {
		t.Fatal("новое обязательство в индексе")
	}
}
