package server

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/ui"
)

type escalationView struct {
	ID         uuid.UUID  `json:"escalation_id"`
	Obligation uuid.UUID  `json:"obligation_id"`
	Reason     string     `json:"reason"`
	Raised     time.Time  `json:"raised_at"`
	Closed     *time.Time `json:"closed_at"`
	By         string     `json:"closed_by"`
	Resolution string     `json:"resolution"`
	Comment    string     `json:"comment"`
}
type dutyScreen struct {
	Escalations            []escalationView
	IncludeClosed          bool
	Items                  []taskView
	Status                 string
	Statuses               []ui.ObligationStatus
	Offset, Previous, Next int
	HasPrevious, HasNext   bool
}
type transportView struct {
	Ref                  string     `json:"transport_ref"`
	Courier              string     `json:"courier_ref"`
	Manual               bool       `json:"manual"`
	Departed             *time.Time `json:"departed_at"`
	Arrived              *time.Time `json:"arrived_at"`
	DepartureTemperature *float64   `json:"temperature_at_dispatch_c"`
	ArrivalTemperature   *float64   `json:"temperature_at_arrival_c"`
	Violated             bool       `json:"conditions_violated"`
	Violations           []string   `json:"violations"`
}

func (s *Server) registerDuty(mux *http.ServeMux) {
	mux.Handle("GET /duty/escalations", s.guard.Require(auth.OpHandleEscalation)(http.HandlerFunc(s.escalationsPage)))
	mux.Handle("POST /duty/escalations/{id}/close", s.guard.Require(auth.OpHandleEscalation)(http.HandlerFunc(s.closeEscalation)))
	mux.Handle("GET /duty/tasks", s.guard.Require(auth.OpHandleEscalation)(http.HandlerFunc(s.allTasks)))
	mux.Handle("POST /tasks/{id}/manual-transport", s.guard.Require(auth.OpHandleEscalation)(http.HandlerFunc(s.manualTransport)))
	mux.Handle("POST /tasks/{id}/dispatch", s.guard.Require(auth.OpDispatchTransfer)(http.HandlerFunc(s.dispatchTransfer)))
	mux.Handle("POST /tasks/{id}/receive", s.guard.Require(auth.OpReceiveTransfer)(http.HandlerFunc(s.receiveTransfer)))
}
func (s *Server) escalationsPage(w http.ResponseWriter, r *http.Request) {
	data := dutyScreen{IncludeClosed: r.URL.Query().Get("include_closed") == "true"}
	if err := s.transfer.GetJSON(r.Context(), "/escalations?include_closed="+strconv.FormatBool(data.IncludeClosed), &data.Escalations); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	s.renderScreen(w, r, 200, "escalations.html", "Эскалации дежурного", data)
}
func (s *Server) closeEscalation(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.renderError(w, r, 400, "Неверная эскалация", "Откройте обращение из списка.")
		return
	}
	if err := s.transfer.PostJSON(r.Context(), "/escalations/"+id.String()+"/close", map[string]string{"resolution": r.FormValue("resolution"), "comment": r.FormValue("comment")}, nil); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	ui.SetFlash(w, s.cfg.CookieSecure, ui.Flash{Tone: ui.ToneSuccess, Text: "Обращение закрыто. Запрет использования компонентов закрытие не снимает."})
	http.Redirect(w, r, "/duty/escalations", http.StatusSeeOther)
}
func (s *Server) allTasks(w http.ResponseWriter, r *http.Request) {
	offset := 0
	if value := r.URL.Query().Get("offset"); value != "" {
		var err error
		offset, err = strconv.Atoi(value)
		if err != nil || offset < 0 {
			s.renderError(w, r, 400, "Неверная страница", "Используйте ссылки списка.")
			return
		}
	}
	data := dutyScreen{Status: r.URL.Query().Get("status"), Statuses: ui.AllObligationStatuses(), Offset: offset, Previous: max(0, offset-100), Next: offset + 100, HasPrevious: offset > 0}
	q := url.Values{"status": {data.Status}, "offset": {strconv.Itoa(offset)}}
	var list struct {
		Items []taskView `json:"items"`
	}
	if err := s.transfer.GetJSON(r.Context(), "/duty/obligations?"+q.Encode(), &list); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	data.Items = list.Items
	data.HasNext = len(list.Items) == 100
	s.renderScreen(w, r, 200, "duty-tasks.html", "Задания всех клиник", data)
}
func (s *Server) manualTransport(w http.ResponseWriter, r *http.Request) {
	task, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	if err := s.transfer.PostJSON(r.Context(), "/obligations/"+task.ID.String()+"/manual-transport", map[string]string{"transport_ref": r.FormValue("transport_ref"), "courier_ref": r.FormValue("courier_ref")}, nil); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	http.Redirect(w, r, "/tasks/"+task.ID.String(), http.StatusSeeOther)
}
func (s *Server) transportInput(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	input := map[string]any{"container_intact": r.FormValue("container_intact") == "true"}
	if value := r.FormValue("temperature_c"); value != "" {
		temperature, err := strconv.ParseFloat(value, 64)
		if err != nil {
			s.renderError(w, r, 400, "Неверная температура", "Введите число градусов Цельсия.")
			return nil, false
		}
		input["temperature_c"] = temperature
	}
	if value := r.FormValue("at"); value != "" {
		at, err := time.Parse("2006-01-02T15:04", value)
		if err != nil {
			s.renderError(w, r, 400, "Неверное время", "Укажите дату и время UTC.")
			return nil, false
		}
		input["at"] = at
	}
	return input, true
}
func (s *Server) dispatchTransfer(w http.ResponseWriter, r *http.Request) {
	task, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	input, ok := s.transportInput(w, r)
	if !ok {
		return
	}
	input["courier_ref"] = r.FormValue("courier_ref")
	if err := s.transfer.PostJSON(r.Context(), "/obligations/"+task.ID.String()+"/dispatch", input, nil); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	http.Redirect(w, r, "/tasks/"+task.ID.String(), http.StatusSeeOther)
}
func (s *Server) receiveTransfer(w http.ResponseWriter, r *http.Request) {
	task, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	input, ok := s.transportInput(w, r)
	if !ok {
		return
	}
	input["temperature_excursion"] = r.FormValue("temperature_excursion") == "true"
	if err := s.transfer.PostJSON(r.Context(), "/obligations/"+task.ID.String()+"/receive", input, nil); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	http.Redirect(w, r, "/tasks/"+task.ID.String(), http.StatusSeeOther)
}
