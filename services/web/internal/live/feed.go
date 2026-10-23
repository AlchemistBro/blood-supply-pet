package live

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
)

// defaultIndexCapacity — сколько обязательств помнить ради клиник (демо: тысячи в сутки).
const defaultIndexCapacity = 50_000

type clinicPair struct{ source, target int64 }

// Feed — обработчик transfer.events для показа. Это не бизнес-обработка: inbox не нужен, повторов и DLT
// нет (ошибка разбора только в лог), рассылаются лишь события, случившиеся после запуска процесса.
//
// Клиники есть только в ObligationOpened, поэтому Feed помнит obligation_id → (источник, получатель)
// в ограниченном индексе. Для обязательства, открытого до того, как этот экземпляр начал читать топик,
// клиники неизвестны: такое событие получают только подписчики без клиники (дежурный).
type Feed struct {
	hub   *Hub
	clock clock.Clock
	since time.Time
	log   *slog.Logger

	mu       sync.Mutex
	index    map[uuid.UUID]clinicPair
	order    []uuid.UUID
	capacity int
}

// NewFeed создаёт обработчик; показываются события не старше момента создания.
func NewFeed(hub *Hub, clk clock.Clock, log *slog.Logger) *Feed {
	return &Feed{hub: hub, clock: clk, since: clk.Now(), log: log, index: map[uuid.UUID]clinicPair{}, capacity: defaultIndexCapacity}
}

// Handle реализует messaging.Handler. Всегда возвращает nil.
func (f *Feed) Handle(_ context.Context, msg messaging.Message) error {
	id, status, ok := f.decode(msg)
	if !ok {
		return nil
	}
	if msg.OccurredAt.Before(f.since) {
		return nil
	}
	if status == "" {
		return nil
	}
	pair, _ := f.clinics(id)
	html, err := renderStatus(id, status)
	if err != nil {
		f.log.Error("фрагмент статуса не отрисован", "type", msg.Type, "err", err)
		return nil
	}
	f.hub.Publish(Event{ObligationID: id, SourceClinicID: pair.source, TargetClinicID: pair.target,
		Status: status, At: msg.OccurredAt, HTML: html})
	return nil
}

// decode разбирает событие: обязательство и новый статус O07 ("" — событие статус не меняет).
// ObligationOpened дополнительно запоминает клиники.
func (f *Feed) decode(msg messaging.Message) (uuid.UUID, string, bool) {
	var id uuid.UUID
	var status string
	var err error
	switch msg.Type {
	case "ObligationOpened":
		var e transfer.ObligationOpened
		if err = msg.Decode(&e); err == nil {
			f.remember(e.ObligationID, e.SourceClinicID, e.TargetClinicID)
			id, status = e.ObligationID, "PENDING"
		}
	case "TransferConfirmed":
		var e transfer.TransferConfirmed
		err = msg.Decode(&e)
		id, status = e.ObligationID, "CONFIRMED"
	case "TransferRejected":
		var e transfer.TransferRejected
		err = msg.Decode(&e)
		id, status = e.ObligationID, "REJECTED"
	case "TransferExpired":
		var e transfer.TransferExpired
		err = msg.Decode(&e)
		id, status = e.ObligationID, "EXPIRED"
	case "TransferCancelled":
		var e transfer.TransferCancelled
		err = msg.Decode(&e)
		id, status = e.ObligationID, "CANCELLED"
	case "TransferBroken":
		var e transfer.TransferBroken
		err = msg.Decode(&e)
		id, status = e.ObligationID, "BROKEN"
	case "TransportRequested":
		var e transfer.TransportRequested
		err = msg.Decode(&e)
		id, status = e.ObligationID, "TRANSPORT_REQUESTED"
	default:
		// Недостача, замена единицы, отказ перевозчика, эскалация статус O07 не меняют; новые типы — тоже мимо.
		return uuid.Nil, "", false
	}
	if err != nil || id == uuid.Nil {
		f.log.Warn("событие transfer не разобрано, пропущено", "type", msg.Type, "id", msg.ID, "err", err)
		return uuid.Nil, "", false
	}
	return id, status, true
}

func (f *Feed) remember(id uuid.UUID, source, target int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.index[id]; !ok {
		f.order = append(f.order, id)
	}
	f.index[id] = clinicPair{source: source, target: target}
	for len(f.order) > f.capacity {
		delete(f.index, f.order[0])
		f.order = f.order[1:]
	}
}

func (f *Feed) clinics(id uuid.UUID) (clinicPair, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.index[id]
	return p, ok
}
