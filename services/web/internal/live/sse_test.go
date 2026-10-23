package live

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/auditclient"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/authz"
)

type fakeAudit struct {
	mu      sync.Mutex
	entries []auditclient.Entry
}

func (f *fakeAudit) Record(_ context.Context, e auditclient.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeAudit) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.entries)
}

var users = map[string]auth.Principal{
	"operator1": {Username: "operator-c001", Roles: []auth.Role{auth.RoleOperator}, ClinicID: clinic(1)},
	"duty":      {Username: "duty1", Roles: []auth.Role{auth.RoleDuty}},
	"analyst":   {Username: "analyst1", Roles: []auth.Role{auth.RoleAnalyst}},
}

type sseEnv struct {
	hub   *Hub
	audit *fakeAudit
	srv   *httptest.Server
}

func newSSEEnv(t *testing.T, heartbeat time.Duration) *sseEnv {
	t.Helper()
	e := &sseEnv{hub: NewHub(8), audit: &fakeAudit{}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	forbidden := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Недостаточно прав", http.StatusForbidden)
	})
	guard := authz.New(e.audit, log, forbidden)
	h := NewSSE(e.hub, guard, log, SSEConfig{Heartbeat: heartbeat, MaxStream: time.Minute}).Handler()
	// Участника в контекст кладёт проверка сессии web; здесь — по заголовку теста.
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := users[r.Header.Get("X-Test-User")]; ok {
			r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *sseEnv) open(t *testing.T, user, query string) (*http.Response, *bufio.Reader, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.srv.URL+"/events/obligations"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Test-User", user)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = resp.Body.Close() })
	return resp, bufio.NewReader(resp.Body), cancel
}

// readUntil читает строки потока, пока не встретит строку с префиксом prefix.
func readUntil(t *testing.T, r *bufio.Reader, prefix string, wait time.Duration) string {
	t.Helper()
	lines := make(chan string, 1)
	go func() {
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				close(lines)
				return
			}
			if strings.HasPrefix(line, prefix) {
				lines <- strings.TrimRight(line, "\n")
				return
			}
		}
	}()
	select {
	case l, ok := <-lines:
		if !ok {
			t.Fatalf("поток закрыт до строки %q", prefix)
		}
		return l
	case <-time.After(wait):
		t.Fatalf("нет строки %q за %v", prefix, wait)
	}
	return ""
}

func waitSubscribers(t *testing.T, h *Hub, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for h.Stats().Subscribers != n {
		if time.Now().After(deadline) {
			t.Fatalf("подписчиков %d, ожидалось %d", h.Stats().Subscribers, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Заголовки SSE (в том числе для nginx) и событие обязательства своей клиники в формате для sse-swap.
func TestSSEHeadersAndEvent(t *testing.T) {
	e := newSSEEnv(t, time.Minute)
	resp, r, _ := e.open(t, "operator1", "?clinic=1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("статус %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type: %s", ct)
	}
	if resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatal("для nginx нужен X-Accel-Buffering: no")
	}
	readUntil(t, r, "retry:", time.Second)
	waitSubscribers(t, e.hub, 1)

	id := uuid.New()
	html, err := renderStatus(id, "TRANSPORT_REQUESTED")
	if err != nil {
		t.Fatal(err)
	}
	e.hub.Publish(Event{ObligationID: uuid.New(), SourceClinicID: 2, TargetClinicID: 3, Status: "CONFIRMED", HTML: "чужое"})
	e.hub.Publish(Event{ObligationID: id, SourceClinicID: 1, TargetClinicID: 3, Status: "TRANSPORT_REQUESTED", HTML: html})
	if got := readUntil(t, r, "event:", time.Second); got != "event: "+EventName(id) {
		t.Fatalf("имя события: %q (событие чужой клиники не должно прийти)", got)
	}
	if got := readUntil(t, r, "data:", time.Second); got != "data: "+html {
		t.Fatalf("данные события: %q", got)
	}
}

// Комментарий-пульс держит соединение через прокси.
func TestSSEHeartbeat(t *testing.T) {
	e := newSSEEnv(t, 20*time.Millisecond)
	_, r, _ := e.open(t, "operator1", "")
	readUntil(t, r, ": ping", time.Second)
	readUntil(t, r, ": ping", time.Second)
}

// Чужая клиника — 403 и запись отказа в аудит.
func TestSSEForeignClinicForbidden(t *testing.T) {
	e := newSSEEnv(t, time.Minute)
	resp, _, _ := e.open(t, "operator1", "?clinic=2")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("чужая клиника: ожидался 403, получен %d", resp.StatusCode)
	}
	if e.audit.count() != 1 || e.audit.entries[0].Action != auditc.ActionAccessDenied ||
		e.audit.entries[0].ObjectID != string(auth.OpViewObligation) {
		t.Fatalf("отказ в аудите: %+v", e.audit.entries)
	}
	if e.hub.Stats().Subscribers != 0 {
		t.Fatal("при отказе подписки нет")
	}
}

// Роль без права VIEW_OBLIGATION — 403.
func TestSSERoleWithoutOperationForbidden(t *testing.T) {
	e := newSSEEnv(t, time.Minute)
	resp, _, _ := e.open(t, "analyst", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("аналитику живые статусы недоступны: %d", resp.StatusCode)
	}
}

func TestSSEBadClinicParameter(t *testing.T) {
	e := newSSEEnv(t, time.Minute)
	resp, _, _ := e.open(t, "operator1", "?clinic=abc")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("неверный параметр clinic: %d", resp.StatusCode)
	}
}

// Дежурный видит любую клинику, без параметра — все события.
func TestSSEDutyOfficerAnyClinic(t *testing.T) {
	e := newSSEEnv(t, time.Minute)
	resp, r, _ := e.open(t, "duty", "?clinic=2")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("дежурному доступна любая клиника: %d", resp.StatusCode)
	}
	readUntil(t, r, "retry:", time.Second)

	resp, r, _ = e.open(t, "duty", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("дежурный без параметра: %d", resp.StatusCode)
	}
	readUntil(t, r, "retry:", time.Second)
	waitSubscribers(t, e.hub, 2)
	id := uuid.New()
	e.hub.Publish(Event{ObligationID: id, Status: "BROKEN", HTML: "<span>x</span>"}) // клиники неизвестны
	if got := readUntil(t, r, "event:", time.Second); got != "event: "+EventName(id) {
		t.Fatalf("дежурный без клиники получает все события: %q", got)
	}
}

// Закрытие соединения браузером отписывает от хаба.
func TestSSEUnsubscribesOnClose(t *testing.T) {
	e := newSSEEnv(t, time.Minute)
	_, r, cancel := e.open(t, "operator1", "?clinic=1")
	readUntil(t, r, "retry:", time.Second)
	waitSubscribers(t, e.hub, 1)
	cancel()
	waitSubscribers(t, e.hub, 0)
}

// Поток закрывается сам до WriteTimeout сервера, если снять дедлайн записи нельзя; браузер переподключится.
func TestSSEMaxStreamWithoutWriteDeadline(t *testing.T) {
	e := &sseEnv{hub: NewHub(8), audit: &fakeAudit{}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	guard := authz.New(e.audit, log, http.NotFoundHandler())
	s := NewSSE(e.hub, guard, log, SSEConfig{Heartbeat: time.Minute, MaxStream: 50 * time.Millisecond})
	rec := httptest.NewRecorder() // не поддерживает SetWriteDeadline
	req := httptest.NewRequest(http.MethodGet, "/events/obligations?clinic=1", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), users["operator1"]))
	done := make(chan struct{})
	go func() { s.Handler().ServeHTTP(rec, req); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("поток без дедлайна записи должен закрываться по MaxStream")
	}
	if e.hub.Stats().Subscribers != 0 {
		t.Fatal("после закрытия потока подписчика нет")
	}
}
