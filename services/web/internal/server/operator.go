package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/svcclient"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/ui"
)

// Узкие DTO экрана намеренно не содержат фенотип и номер случая (ADR-0011, ADR-0020).
type taskView struct {
	ID        uuid.UUID   `json:"obligation_id"`
	Source    int64       `json:"source_clinic_id"`
	Target    int64       `json:"target_clinic_id"`
	Kind      string      `json:"component_kind"`
	Group     string      `json:"blood_group"`
	Requested int         `json:"requested_qty"`
	Confirmed int         `json:"confirmed_qty"`
	Status    string      `json:"status"`
	Urgency   string      `json:"urgency"`
	Deadline  time.Time   `json:"deadline"`
	Units     []uuid.UUID `json:"units"`
	Spec      struct {
		PhenotypeRequired bool `json:"phenotype_required"`
	} `json:"spec"`
}

func (t taskView) StatusBadge() ui.Badge { return ui.StatusBadge(ui.ObligationStatus(t.Status)) }
func (t taskView) Pending() bool         { return t.Status == "PENDING" }
func (t taskView) Replaceable() bool {
	return t.Status == "CONFIRMED" || t.Status == "TRANSPORT_REQUESTED"
}

type candidateView struct {
	ClinicID  int64     `json:"clinic_id"`
	Status    string    `json:"status"`
	ID        uuid.UUID `json:"unit_id"`
	Kind      string    `json:"component_kind"`
	Group     string    `json:"blood_group"`
	Volume    int64     `json:"volume_ml"`
	Expires   time.Time `json:"expires_at"`
	Location  string    `json:"storage_location"`
	Available bool      `json:"available"`
}
type taskScreen struct {
	History    []historyView
	Items      []taskView
	Task       taskView
	Candidates []candidateView
	Key        string
	Clinic     int64
	Status     string
	Statuses   []ui.ObligationStatus
	Message    string
	CanAct     bool
}

type historyView struct {
	At     time.Time `json:"occurred_at"`
	Action string    `json:"action"`
	Result string    `json:"result"`
}

func (h historyView) Label() string {
	if label, ok := map[string]string{"OPENED": "Задание открыто", "CONFIRMED": "Передача подтверждена", "REJECTED": "Источник отказал в передаче", "EXPIRED": "Срок подтверждения истёк", "CANCELLED": "Задание отменено", "BROKEN": "Обязательство нарушено", "TRANSPORT_REQUESTED": "Перевозка запрошена", "UNIT_REPLACED": "Единица заменена"}[h.Action]; ok {
		return label
	}
	return h.Action
}

func (s *Server) registerOperator(mux *http.ServeMux) {
	mux.Handle("GET /tasks", s.guard.Require(auth.OpViewTasks)(http.HandlerFunc(s.taskList)))
	mux.Handle("GET /tasks/{id}", s.guard.Require(auth.OpViewObligation)(http.HandlerFunc(s.taskCard)))
	mux.Handle("POST /tasks/{id}/confirm", s.guard.Require(auth.OpConfirmTransfer)(http.HandlerFunc(s.confirmTask)))
	mux.Handle("POST /tasks/{id}/replace", s.guard.Require(auth.OpReplaceUnit)(http.HandlerFunc(s.replaceTask)))
	mux.Handle("POST /tasks/{id}/reject", s.guard.Require(auth.OpRejectTransfer)(http.HandlerFunc(s.rejectTask)))
}

func (s *Server) screenPage(r *http.Request, title string) ui.Page {
	p, _ := auth.FromContext(r.Context())
	d, _ := currentSession(r.Context())
	page := ui.Page{Title: title, Header: ui.Header{User: ui.User{Login: p.Username, Role: auth.Role(p.PrimaryRole())}, Freshness: ui.UnknownFreshness()},
		Footer: ui.Footer{Version: s.cfg.Version}, CSRFToken: d.CSRF}
	if p.ClinicID != nil {
		page.Header.Clinic = fmt.Sprintf("Клиника № %d", *p.ClinicID)
	}
	if auth.Allowed(auth.OpViewTasks, p.Roles) {
		page.Nav = append(page.Nav, ui.NavItem{Label: "Задания на передачу", URL: "/tasks", Active: strings.HasPrefix(r.URL.Path, "/tasks")})
	}
	if s.inventory != nil && auth.Allowed(auth.OpViewStock, p.Roles) {
		page.Nav = append(page.Nav, ui.NavItem{Label: "Запас клиники", URL: "/stock", Active: strings.HasPrefix(r.URL.Path, "/stock")})
		var sync struct {
			Watermark *time.Time `json:"watermark"`
		}
		if err := s.inventory.GetJSON(r.Context(), "/sync/last", &sync); err == nil && sync.Watermark != nil {
			page.Header.Freshness = ui.NewFreshness(*sync.Watermark, s.clock.Now(), 5*time.Minute)
		}
	}
	return page
}

func (s *Server) renderScreen(w http.ResponseWriter, r *http.Request, status int, name, title string, data any) {
	p := s.screenPage(r, title)
	p.Content = data
	var buf bytes.Buffer
	if err := s.ui.Page(&buf, name, p); err != nil {
		s.log.ErrorContext(r.Context(), "экран не отрисован", "page", name, "err", err)
		s.renderError(w, r, 500, "Экран недоступен", "Повторите позже.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) serviceFailure(w http.ResponseWriter, r *http.Request, err error) {
	if svcclient.LoginOnUnauthorized(w, r, err) {
		return
	}
	var statusErr *svcclient.StatusError
	if errors.As(err, &statusErr) && (statusErr.Code == 403 || statusErr.Code == 404) {
		s.renderError(w, r, statusErr.Code, "Задание недоступно", "Проверьте клинику и обновите список заданий.")
		return
	}
	s.renderError(w, r, 503, "Сервис временно недоступен", "Не удалось получить данные. Повторите позже.")
}

func (s *Server) taskList(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	clinic := int64(0)
	if p.ClinicID != nil {
		clinic = *p.ClinicID
	}
	if p.HasRole(auth.RoleDuty) && r.URL.Query().Get("clinic") != "" {
		clinic, _ = strconv.ParseInt(r.URL.Query().Get("clinic"), 10, 64)
	}
	data := taskScreen{Clinic: clinic, Statuses: ui.AllObligationStatuses(), Status: r.URL.Query().Get("status")}
	if clinic <= 0 {
		s.renderScreen(w, r, 200, "tasks.html", "Задания на передачу", data)
		return
	}
	q := url.Values{"sourceClinicId": {strconv.FormatInt(clinic, 10)}}
	if data.Status != "" {
		known := false
		for _, st := range data.Statuses {
			if string(st) == data.Status {
				known = true
			}
		}
		if !known {
			s.renderError(w, r, 400, "Неверный фильтр", "Выберите статус из списка.")
			return
		}
		q.Set("status", data.Status)
	}
	var list struct {
		Items []taskView `json:"items"`
	}
	if err := s.transfer.GetJSON(r.Context(), "/obligations?"+q.Encode(), &list); err != nil {
		s.serviceFailure(w, r, err)
		return
	}
	data.Items = list.Items
	s.renderScreen(w, r, 200, "tasks.html", "Задания на передачу", data)
}

func (s *Server) loadTask(w http.ResponseWriter, r *http.Request) (taskView, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.renderError(w, r, 400, "Неверный номер задания", "Откройте задание из списка.")
		return taskView{}, false
	}
	var task taskView
	if err := s.transfer.GetJSON(r.Context(), "/obligations/"+id.String(), &task); err != nil {
		s.serviceFailure(w, r, err)
		return taskView{}, false
	}
	return task, true
}

func (s *Server) taskCard(w http.ResponseWriter, r *http.Request) {
	task, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	s.showTask(w, r, 200, task, "")
}

func (s *Server) showTask(w http.ResponseWriter, r *http.Request, status int, task taskView, message string) {
	p, _ := auth.FromContext(r.Context())
	data := taskScreen{Task: task, Key: uuid.NewString(), Message: message, CanAct: auth.Allowed(auth.OpConfirmTransfer, p.Roles) && auth.SameClinic(p, task.Source)}
	var history struct {
		Items []historyView `json:"items"`
	}
	if err := s.transfer.GetJSON(r.Context(), "/obligations/"+task.ID.String()+"/history?limit=50", &history); err != nil {
		s.serviceFailure(w, r, err)
		return
	}
	data.History = history.Items
	if data.CanAct && (task.Pending() || task.Replaceable()) {
		q := url.Values{"kind": {task.Kind}, "fefo": {"true"}, "limit": {"100"}}
		if task.Group != "" {
			q.Set("group", task.Group)
		}
		var candidates struct {
			Units []candidateView `json:"units"`
		}
		if err := s.inventory.GetJSON(r.Context(), fmt.Sprintf("/clinics/%d/units?%s", task.Source, q.Encode()), &candidates); err != nil {
			s.serviceFailure(w, r, err)
			return
		}
		for _, candidate := range candidates.Units {
			if candidate.Available {
				data.Candidates = append(data.Candidates, candidate)
			}
		}
	}
	s.renderScreen(w, r, status, "task.html", "Задание на передачу", data)
}

func (s *Server) taskAction(w http.ResponseWriter, r *http.Request, action string, body any) {
	task, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	p, _ := auth.FromContext(r.Context())
	if !auth.SameClinic(p, task.Source) {
		s.guard.Deny(w, r, p, auth.OpConfirmTransfer, "clinic")
		return
	}
	raw, err := json.Marshal(body)
	if err != nil {
		s.serviceFailure(w, r, err)
		return
	}
	resp, err := s.transfer.Do(r.Context(), http.MethodPost, "/obligations/"+task.ID.String()+"/"+action, bytes.NewReader(raw), "application/json")
	if err != nil {
		s.serviceFailure(w, r, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	var result struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil || result.Message == "" {
		s.renderError(w, r, 503, "Ответ не получен", "Обновите карточку перед повторным действием.")
		return
	}
	if resp.StatusCode != 200 && resp.StatusCode != 409 && resp.StatusCode != 422 && resp.StatusCode != 503 {
		s.renderError(w, r, resp.StatusCode, "Действие не выполнено", "Обновите карточку задания.")
		return
	}
	// После изменения перечитывается состояние: повтор формы сохраняет ключ идемпотентности transfer.
	if fresh, ok := s.loadTask(w, r); ok {
		s.showTask(w, r, resp.StatusCode, fresh, result.Message)
	}
}

func (s *Server) confirmTask(w http.ResponseWriter, r *http.Request) {
	ids := r.PostForm["unit_ids"]
	if len(ids) == 0 || len(ids) > 100 {
		s.renderError(w, r, 400, "Не выбраны единицы", "Выберите от 1 до 100 единиц компонента крови.")
		return
	}
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil {
			s.renderError(w, r, 400, "Неверная единица", "Обновите список единиц.")
			return
		}
	}
	key := r.PostFormValue("idempotency_key")
	if key == "" || len(key) > 128 {
		s.renderError(w, r, 400, "Форма устарела", "Обновите карточку задания.")
		return
	}
	s.taskAction(w, r, "confirm", struct {
		Units   []string `json:"unit_ids"`
		Key     string   `json:"idempotency_key"`
		Partial bool     `json:"accept_partial"`
	}{ids, key, r.PostFormValue("accept_partial") == "true"})
}

func (s *Server) replaceTask(w http.ResponseWriter, r *http.Request) {
	oldID, errOld := uuid.Parse(r.PostFormValue("old_unit_id"))
	newID, errNew := uuid.Parse(r.PostFormValue("new_unit_id"))
	if errOld != nil || errNew != nil {
		s.renderError(w, r, 400, "Не выбраны единицы", "Выберите закреплённую единицу и единицу для замены.")
		return
	}
	s.taskAction(w, r, "replace", struct {
		Old uuid.UUID `json:"old_unit_id"`
		New uuid.UUID `json:"new_unit_id"`
	}{oldID, newID})
}

func (s *Server) rejectTask(w http.ResponseWriter, r *http.Request) {
	if r.PostFormValue("reason") != "SOURCE_REFUSED" {
		s.renderError(w, r, 400, "Не указана причина", "Выберите причину отказа.")
		return
	}
	s.taskAction(w, r, "reject", map[string]string{"reason": "SOURCE_REFUSED"})
}
