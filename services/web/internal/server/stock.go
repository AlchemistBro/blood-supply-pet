package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
)

type stockLine struct {
	Kind     string    `json:"component_kind"`
	Group    string    `json:"blood_group"`
	Count    int64     `json:"unit_count"`
	VolumeML int64     `json:"volume_ml"`
	VolumeL  float64   `json:"volume_l"`
	Expires  time.Time `json:"nearest_expiry"`
	NormML   float64
	Below    bool
}
type normView struct {
	Kind    string  `json:"component_kind"`
	Year    int     `json:"consumption_year"`
	Annual  int64   `json:"annual_volume_ml"`
	Monthly float64 `json:"avg_monthly_ml"`
	Norm    float64 `json:"norm_ml"`
	Minimum bool    `json:"is_min_unit_applied"`
	Version int64   `json:"version"`
}
type stockScreen struct {
	PrevURL, NextURL string
	Clinic           int64
	Lines            []stockLine
	Norms            []normView
	Units            []candidateView
	Message          string
	CanEdit          bool
	Year             int
}

func (s *Server) registerStock(mux *http.ServeMux) {
	mux.Handle("GET /stock", s.guard.Require(auth.OpViewStock)(http.HandlerFunc(s.stockPage)))
	mux.Handle("GET /stock/units", s.guard.Require(auth.OpViewStock)(http.HandlerFunc(s.stockUnits)))
	mux.Handle("GET /stock/units/{id}", s.guard.Require(auth.OpViewStock)(http.HandlerFunc(s.stockUnit)))
	mux.Handle("POST /stock/sync", s.guard.Require(auth.OpSyncOnDemand)(http.HandlerFunc(s.syncStock)))
	mux.Handle("POST /stock/annual-consumption", s.guard.Require(auth.OpEnterConsumption)(http.HandlerFunc(s.setConsumption)))
}

func (s *Server) stockClinic(w http.ResponseWriter, r *http.Request) (int64, bool) {
	p, _ := auth.FromContext(r.Context())
	clinic := int64(0)
	if p.ClinicID != nil {
		clinic = *p.ClinicID
	}
	param := r.FormValue("clinic")
	if param != "" {
		id, err := strconv.ParseInt(param, 10, 64)
		if err != nil || id <= 0 {
			s.renderError(w, r, 400, "Неверная клиника", "Укажите номер клиники из справочника.")
			return 0, false
		}
		clinic = id
	}
	if clinic <= 0 && (p.HasRole(auth.RoleDuty) || p.HasRole(auth.RoleITService)) {
		return 0, true
	}
	if !auth.SameClinic(p, clinic) {
		s.guard.Deny(w, r, p, auth.OpViewStock, "clinic")
		return 0, false
	}
	return clinic, true
}

func (s *Server) stockPage(w http.ResponseWriter, r *http.Request) {
	clinic, ok := s.stockClinic(w, r)
	if !ok {
		return
	}
	s.showStock(w, r, clinic, "")
}

func (s *Server) showStock(w http.ResponseWriter, r *http.Request, clinic int64, message string) {
	p, _ := auth.FromContext(r.Context())
	data := stockScreen{Clinic: clinic, Message: message, CanEdit: auth.Allowed(auth.OpEnterConsumption, p.Roles), Year: s.clock.Now().Year()}
	if clinic > 0 {
		var stock struct {
			Lines []stockLine `json:"lines"`
		}
		if err := s.inventory.GetJSON(r.Context(), fmt.Sprintf("/clinics/%d/stock", clinic), &stock); err != nil {
			s.serviceFailure(w, r, err)
			return
		}
		if err := s.inventory.GetJSON(r.Context(), fmt.Sprintf("/clinics/%d/norms", clinic), &data.Norms); err != nil {
			s.serviceFailure(w, r, err)
			return
		}
		totals := map[string]int64{}
		for _, line := range stock.Lines {
			totals[line.Kind] += line.VolumeML
		}
		for i := range stock.Lines {
			for _, norm := range data.Norms {
				if norm.Kind == stock.Lines[i].Kind {
					stock.Lines[i].NormML = norm.Norm
					stock.Lines[i].Below = float64(totals[norm.Kind]) < norm.Norm
				}
			}
		}
		data.Lines = stock.Lines
		for _, norm := range data.Norms {
			if _, known := totals[norm.Kind]; !known {
				data.Lines = append(data.Lines, stockLine{Kind: norm.Kind, Group: "Все", NormML: norm.Norm, Below: norm.Norm > 0})
			}
		}
	}
	s.renderScreen(w, r, 200, "stock.html", "Запас клиники", data)
}

func (s *Server) stockUnits(w http.ResponseWriter, r *http.Request) {
	clinic, ok := s.stockClinic(w, r)
	if !ok {
		return
	}
	data := stockScreen{Clinic: clinic}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 || value > 1_000_000 {
			s.renderError(w, r, 400, "Неверная страница", "Откройте список единиц заново.")
			return
		}
		offset = value
	}
	if clinic > 0 {
		q := url.Values{"limit": {"100"}, "fefo": {"true"}, "offset": {strconv.Itoa(offset)}}
		for _, key := range []string{"kind", "group"} {
			if value := r.URL.Query().Get(key); value != "" {
				q.Set(key, value)
			}
		}
		var page struct {
			Units   []candidateView `json:"units"`
			HasMore bool            `json:"has_more"`
		}
		if err := s.inventory.GetJSON(r.Context(), fmt.Sprintf("/clinics/%d/units?%s", clinic, q.Encode()), &page); err != nil {
			s.serviceFailure(w, r, err)
			return
		}
		data.Units = page.Units
		q.Set("clinic", strconv.FormatInt(clinic, 10))
		if offset > 0 {
			q.Set("offset", strconv.Itoa(max(offset-100, 0)))
			data.PrevURL = "/stock/units?" + q.Encode()
		}
		if page.HasMore {
			q.Set("offset", strconv.Itoa(offset+100))
			data.NextURL = "/stock/units?" + q.Encode()
		}
	}
	s.renderScreen(w, r, 200, "stock-units.html", "Единицы компонента крови (FEFO)", data)
}

func (s *Server) stockUnit(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.renderError(w, r, 400, "Неверная единица", "Откройте единицу из списка.")
		return
	}
	var data struct {
		Unit    candidateView
		History []struct {
			Status  string    `json:"status"`
			At      time.Time `json:"received_at"`
			Version int64     `json:"source_version"`
		}
	}
	base := "/units/" + id.String()
	if err := s.inventory.GetJSON(r.Context(), base, &data.Unit); err != nil {
		s.serviceFailure(w, r, err)
		return
	}
	var history struct {
		Items []struct {
			Status  string    `json:"status"`
			At      time.Time `json:"received_at"`
			Version int64     `json:"source_version"`
		} `json:"items"`
	}
	if err := s.inventory.GetJSON(r.Context(), base+"/history", &history); err != nil {
		s.serviceFailure(w, r, err)
		return
	}
	data.History = history.Items
	s.renderScreen(w, r, 200, "stock-unit.html", "Карточка единицы компонента крови", data)
}

func (s *Server) syncStock(w http.ResponseWriter, r *http.Request) {
	clinic, ok := s.stockClinic(w, r)
	if !ok {
		return
	}
	var result struct {
		Result  string `json:"result"`
		Applied int    `json:"applied"`
		Skipped bool   `json:"skipped"`
	}
	if err := s.inventory.PostJSON(r.Context(), fmt.Sprintf("/clinics/%d/sync", clinic), struct{}{}, &result); err != nil {
		s.serviceFailure(w, r, err)
		return
	}
	message := fmt.Sprintf("Обновление завершено: %s, изменений %d.", result.Result, result.Applied)
	if result.Skipped {
		message = "Обновление уже выполняется. Повторите просмотр через несколько секунд."
	}
	s.showStock(w, r, clinic, message)
}

func (s *Server) setConsumption(w http.ResponseWriter, r *http.Request) {
	clinic, ok := s.stockClinic(w, r)
	if !ok {
		return
	}
	year, e1 := strconv.Atoi(r.PostFormValue("consumption_year"))
	annual, e2 := strconv.ParseInt(r.PostFormValue("annual_volume_ml"), 10, 64)
	if e1 != nil || e2 != nil || annual < 0 || annual > 1_000_000_000_000 || year < 2000 || year > 2100 {
		s.renderError(w, r, 400, "Неверный расход", "Укажите год и неотрицательный объём в мл.")
		return
	}
	body := map[string]any{"component_kind": r.PostFormValue("component_kind"), "consumption_year": year, "annual_volume_ml": annual}
	var result normView
	if err := s.inventory.PutJSON(r.Context(), fmt.Sprintf("/clinics/%d/annual-consumption", clinic), body, &result); err != nil {
		s.serviceFailure(w, r, err)
		return
	}
	s.showStock(w, r, clinic, "Годовой расход и норматив сохранены. Формулы демонстрационные.")
}
