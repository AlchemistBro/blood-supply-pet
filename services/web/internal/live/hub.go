// Package live — живые статусы обязательств (O07) в интерфейсе: события transfer.events из Kafka
// превращаются во фрагменты HTML и рассылаются браузерам по SSE (ADR-0007). Хаб в памяти процесса:
// у каждого экземпляра web свой, состояние не разделяется (ADR-0018).
package live

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// Event — изменение статуса обязательства, готовое к показу.
type Event struct {
	ObligationID uuid.UUID
	// SourceClinicID, TargetClinicID — клиники обязательства; 0 — неизвестна.
	SourceClinicID int64
	TargetClinicID int64
	Status         string
	At             time.Time
	// HTML — фрагмент для hx-swap (строка статуса); без фенотипа и номера случая.
	HTML string
}

// Subscriber — одно SSE-соединение.
type Subscriber struct {
	clinic *int64
	ch     chan Event
	done   chan struct{}
	once   sync.Once
}

// Events — канал событий подписчика.
func (s *Subscriber) Events() <-chan Event { return s.ch }

// Done закрывается при отписке или отключении медленного подписчика.
func (s *Subscriber) Done() <-chan struct{} { return s.done }

func (s *Subscriber) matches(e Event) bool {
	if s.clinic == nil {
		return true
	}
	c := *s.clinic
	return c != 0 && (e.SourceClinicID == c || e.TargetClinicID == c)
}

func (s *Subscriber) close() { s.once.Do(func() { close(s.done) }) }

// Stats — состояние хаба (для тестов и /metrics).
type Stats struct {
	Subscribers int
	// Dropped — события, не доставленные из-за переполнения буфера (подписчик при этом отключён).
	Dropped int64
}

// Hub рассылает события подписчикам. Publish не блокируется: медленный подписчик с полным буфером
// отключается (браузер переподключится сам и перечитает страницу), а не задерживает остальных.
type Hub struct {
	buffer  int
	mu      sync.Mutex
	subs    map[*Subscriber]struct{}
	dropped atomic.Int64

	mSubs    metric.Int64UpDownCounter
	mDropped metric.Int64Counter
}

// NewHub создаёт хаб с буфером buffer событий на подписчика.
func NewHub(buffer int) *Hub {
	if buffer <= 0 {
		buffer = 64
	}
	h := &Hub{buffer: buffer, subs: map[*Subscriber]struct{}{}}
	meter := otel.Meter("blood-supply/web/live")
	// Ошибка создания инструмента не мешает работе: остаются Stats.
	h.mSubs, _ = meter.Int64UpDownCounter("web_sse_subscribers", metric.WithDescription("Открытые SSE-соединения живых статусов"))
	h.mDropped, _ = meter.Int64Counter("web_sse_dropped_events", metric.WithDescription("События, отброшенные из-за медленного подписчика"))
	return h
}

// Subscribe добавляет подписчика. clinic == nil — все клиники (дежурный).
func (h *Hub) Subscribe(clinic *int64) *Subscriber {
	s := &Subscriber{clinic: clinic, ch: make(chan Event, h.buffer), done: make(chan struct{})}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	h.addSubs(1)
	return s
}

// Unsubscribe удаляет подписчика; повторный вызов безопасен.
func (h *Hub) Unsubscribe(s *Subscriber) {
	h.mu.Lock()
	_, ok := h.subs[s]
	delete(h.subs, s)
	h.mu.Unlock()
	if ok {
		h.addSubs(-1)
	}
	s.close()
}

// Publish рассылает событие подходящим подписчикам без блокировки.
func (h *Hub) Publish(e Event) {
	var slow []*Subscriber
	h.mu.Lock()
	for s := range h.subs {
		if !s.matches(e) {
			continue
		}
		select {
		case s.ch <- e:
		default:
			delete(h.subs, s)
			slow = append(slow, s)
		}
	}
	h.mu.Unlock()
	for _, s := range slow {
		s.close()
		h.dropped.Add(1)
		h.addSubs(-1)
		if h.mDropped != nil {
			h.mDropped.Add(context.Background(), 1)
		}
	}
}

// Stats возвращает число подписчиков и отброшенных событий.
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	n := len(h.subs)
	h.mu.Unlock()
	return Stats{Subscribers: n, Dropped: h.dropped.Load()}
}

func (h *Hub) addSubs(d int64) {
	if h.mSubs != nil {
		h.mSubs.Add(context.Background(), d)
	}
}
