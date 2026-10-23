package live

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/authz"
)

// SSEConfig — параметры потока.
type SSEConfig struct {
	// Heartbeat — период комментария-пульса: прокси и балансировщики не рвут «молчащее» соединение.
	Heartbeat time.Duration
	// MaxStream — сколько держать поток, если снять дедлайн записи сервера нельзя (WriteTimeout 60 с
	// в libs/httpx): поток закрывается раньше, браузер переподключается сам через retry.
	MaxStream time.Duration
}

// SSE — GET /events/obligations?clinic=: живые статусы обязательств клиники (источник или получатель).
type SSE struct {
	hub   *Hub
	guard *authz.Guard
	log   *slog.Logger
	cfg   SSEConfig
}

// NewSSE создаёт обработчик потока.
func NewSSE(hub *Hub, guard *authz.Guard, log *slog.Logger, cfg SSEConfig) *SSE {
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = 15 * time.Second
	}
	if cfg.MaxStream <= 0 {
		cfg.MaxStream = 50 * time.Second
	}
	return &SSE{hub: hub, guard: guard, log: log, cfg: cfg}
}

// Handler — обработчик с проверкой права VIEW_OBLIGATION.
func (s *SSE) Handler() http.Handler {
	return s.guard.Require(auth.OpViewObligation)(http.HandlerFunc(s.serve))
}

// allClinics — роли, которые видят обязательства любой клиники (security.md: дежурный и ИТ-служба — все).
func allClinics(p auth.Principal) bool {
	return p.HasRole(auth.RoleDuty) || p.HasRole(auth.RoleITService)
}

// subscription выбирает клинику подписки: параметр clinic, иначе клиника из токена; nil — все клиники.
// Возвращает ok=false, если клиника чужая.
func subscription(p auth.Principal, param string) (clinic *int64, ok bool, err error) {
	if param == "" {
		if p.ClinicID != nil {
			c := *p.ClinicID
			return &c, true, nil
		}
		return nil, allClinics(p), nil
	}
	id, err := strconv.ParseInt(param, 10, 64)
	if err != nil || id <= 0 {
		return nil, false, fmt.Errorf("параметр clinic: %q", param)
	}
	if allClinics(p) || (p.ClinicID != nil && *p.ClinicID == id) {
		return &id, true, nil
	}
	return nil, false, nil
}

func (s *SSE) serve(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	clinic, ok, err := subscription(p, r.URL.Query().Get("clinic"))
	if err != nil {
		http.Error(w, "Неверный номер клиники в адресе.", http.StatusBadRequest)
		return
	}
	if !ok {
		s.guard.Deny(w, r, p, auth.OpViewObligation, "clinic")
		return
	}

	rc := http.NewResponseController(w)
	// Поток живёт дольше WriteTimeout сервера: дедлайн записи снимается; если обёртки не дают — MaxStream.
	var limit <-chan time.Time
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		timer := time.NewTimer(s.cfg.MaxStream)
		defer timer.Stop()
		limit = timer.C
	}

	sub := s.hub.Subscribe(clinic)
	defer s.hub.Unsubscribe(sub)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("X-Accel-Buffering", "no") // nginx не буферизует поток
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, "retry: 3000\n: connected\n\n"); err != nil {
		return
	}
	if err := rc.Flush(); err != nil {
		s.log.ErrorContext(r.Context(), "SSE: ответ не поддерживает сброс буфера", "err", err)
		return
	}

	ticker := time.NewTicker(s.cfg.Heartbeat)
	defer ticker.Stop()
	for {
		var err error
		select {
		case <-r.Context().Done():
			return
		case <-sub.Done():
			// Медленный подписчик отключён хабом: браузер переподключится и получит свежие данные страницы.
			return
		case <-limit:
			return
		case <-ticker.C:
			_, err = io.WriteString(w, ": ping\n\n")
		case e := <-sub.Events():
			err = writeEvent(w, EventName(e.ObligationID), e.HTML)
		}
		if err == nil {
			err = rc.Flush()
		}
		if err != nil {
			return
		}
	}
}

// writeEvent пишет событие SSE; каждая строка данных — отдельное поле data.
func writeEvent(w io.Writer, name, data string) error {
	var b strings.Builder
	b.WriteString("event: ")
	b.WriteString(name)
	b.WriteByte('\n')
	for _, line := range strings.Split(data, "\n") {
		b.WriteString("data: ")
		b.WriteString(strings.TrimRight(line, "\r"))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	_, err := io.WriteString(w, b.String())
	return err
}
