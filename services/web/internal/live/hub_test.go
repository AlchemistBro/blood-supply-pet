package live

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func clinic(id int64) *int64 { return &id }

func ev(source, target int64) Event {
	return Event{ObligationID: uuid.New(), SourceClinicID: source, TargetClinicID: target, Status: "CONFIRMED", HTML: "<span>x</span>"}
}

// recv ждёт событие или сообщает, что его нет.
func recv(s *Subscriber, wait time.Duration) (Event, bool) {
	select {
	case e := <-s.Events():
		return e, true
	case <-time.After(wait):
		return Event{}, false
	}
}

// Подписчик клиники получает события, где клиника — источник или получатель; подписчик без клиники — все.
func TestHubFiltersByClinic(t *testing.T) {
	h := NewHub(8)
	c1, c2, all := h.Subscribe(clinic(1)), h.Subscribe(clinic(2)), h.Subscribe(nil)
	defer h.Unsubscribe(c1)
	defer h.Unsubscribe(c2)
	defer h.Unsubscribe(all)

	e := ev(1, 3)
	h.Publish(e)
	if got, ok := recv(c1, time.Second); !ok || got.ObligationID != e.ObligationID {
		t.Fatal("клиника-источник получает событие")
	}
	if got, ok := recv(all, time.Second); !ok || got.ObligationID != e.ObligationID {
		t.Fatal("подписчик без клиники получает все события")
	}
	if _, ok := recv(c2, 50*time.Millisecond); ok {
		t.Fatal("чужая клиника не получает событие")
	}

	e = ev(3, 2)
	h.Publish(e)
	if got, ok := recv(c2, time.Second); !ok || got.ObligationID != e.ObligationID {
		t.Fatal("клиника-получатель получает событие")
	}
	if _, ok := recv(c1, 50*time.Millisecond); ok {
		t.Fatal("клиника 1 не участвует в обязательстве 3 → 2")
	}
}

// Клиники обязательства неизвестны (ObligationOpened не видели) — только подписчикам без клиники.
func TestHubUnknownClinicGoesOnlyToAll(t *testing.T) {
	h := NewHub(8)
	c1, all := h.Subscribe(clinic(1)), h.Subscribe(nil)
	h.Publish(ev(0, 0))
	if _, ok := recv(all, time.Second); !ok {
		t.Fatal("дежурный видит событие с неизвестной клиникой")
	}
	if _, ok := recv(c1, 50*time.Millisecond); ok {
		t.Fatal("подписчик клиники не видит событие без известной клиники")
	}
}

func TestHubUnsubscribe(t *testing.T) {
	h := NewHub(8)
	s := h.Subscribe(clinic(1))
	if h.Stats().Subscribers != 1 {
		t.Fatal("подписчик учтён")
	}
	h.Unsubscribe(s)
	h.Unsubscribe(s) // повторная отписка безопасна
	if h.Stats().Subscribers != 0 {
		t.Fatal("после отписки подписчиков нет")
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("Done закрыт после отписки")
	}
	h.Publish(ev(1, 2)) // не паникует и не блокирует
	if _, ok := recv(s, 50*time.Millisecond); ok {
		t.Fatal("после отписки событий нет")
	}
}

// Медленный подписчик (буфер полон) отключается, остальные получают все события, хаб не блокируется.
func TestHubDisconnectsSlowSubscriber(t *testing.T) {
	h := NewHub(2)
	slow := h.Subscribe(nil)
	fast := h.Subscribe(nil)

	got := make(chan int, 1)
	go func() {
		n := 0
		for n < 3 {
			if _, ok := recv(fast, time.Second); !ok {
				break
			}
			n++
		}
		got <- n
	}()

	done := make(chan struct{})
	go func() {
		for range 3 {
			h.Publish(ev(1, 2))
			time.Sleep(10 * time.Millisecond)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("медленный подписчик заблокировал хаб")
	}

	select {
	case <-slow.Done():
	case <-time.After(time.Second):
		t.Fatal("медленный подписчик отключён")
	}
	if n := <-got; n != 3 {
		t.Fatalf("быстрый подписчик получил %d из 3", n)
	}
	st := h.Stats()
	if st.Subscribers != 1 || st.Dropped != 1 {
		t.Fatalf("статистика: %+v", st)
	}
}
