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
// Клиники берутся из полей события (source_clinic_id, target_clinic_id). Для событий без них (выпущенных
// до расширения контракта) запасной путь — ограниченный индекс obligation_id → (источник, получатель),
// который пополняется из событий с клиниками. Если клиники неизвестны, событие получают только
// подписчики без клиники (дежурный), чтобы статус не ушёл чужой клинике.
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

// clinicFields — необязательные клиники обязательства во всех событиях transfer.events (обратно совместимое
// расширение контракта). Читаются своим узким DTO, а не типами libs/contracts: код собирается и до, и после
// обновления контрактов, а старые события без полей обрабатываются через индекс.
type clinicFields struct {
	SourceClinicID *int64 `json:"source_clinic_id"`
	TargetClinicID *int64 `json:"target_clinic_id"`
}

// Handle реализует messaging.Handler. Всегда возвращает nil.
func (f *Feed) Handle(_ context.Context, msg messaging.Message) error {
	id, status, ok := f.decode(msg)
	if !ok {
		return nil
	}
	pair, known := f.clinicsFromEvent(msg)
	if known {
		f.remember(id, pair.source, pair.target)
	} else {
		pair, _ = f.clinics(id)
	}
	// События до запуска экземпляра (повтор после перезапуска с закоммиченного offset) не показываются:
	// страница при открытии и так берёт текущий статус из transfer.
	if msg.OccurredAt.Before(f.since) {
		return nil
	}
	html, err := renderStatus(id, status)
	if err != nil {
		f.log.Error("фрагмент статуса не отрисован", "type", msg.Type, "err", err)
		return nil
	}
	f.hub.Publish(Event{ObligationID: id, SourceClinicID: pair.source, TargetClinicID: pair.target,
		Status: status, At: msg.OccurredAt, HTML: html})
	return nil
}

// decode разбирает событие: обязательство и новый статус O07.
func (f *Feed) decode(msg messaging.Message) (uuid.UUID, string, bool) {
	var id uuid.UUID
	var status string
	var err error
	switch msg.Type {
	case "ObligationOpened":
		// Клиники ObligationOpened читает clinicsFromEvent, как и у остальных событий.
		var e transfer.ObligationOpened
		err = msg.Decode(&e)
		id, status = e.ObligationID, "PENDING"
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

// clinicsFromEvent — клиники из полей самого события; false, если их там нет.
func (f *Feed) clinicsFromEvent(msg messaging.Message) (clinicPair, bool) {
	var c clinicFields
	if err := msg.Decode(&c); err != nil {
		return clinicPair{}, false
	}
	var p clinicPair
	if c.SourceClinicID != nil {
		p.source = *c.SourceClinicID
	}
	if c.TargetClinicID != nil {
		p.target = *c.TargetClinicID
	}
	return p, p.source > 0 || p.target > 0
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
