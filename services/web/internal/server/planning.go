package server

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
)

type planProposalView struct {
	ID          uuid.UUID `json:"proposal_id"`
	Source      int64     `json:"source_clinic_id"`
	Target      int64     `json:"target_clinic_id"`
	Kind        string    `json:"component_kind"`
	Group       string    `json:"blood_group"`
	Units       int       `json:"units"`
	Minutes     int       `json:"delivery_minutes"`
	Explanation string    `json:"explanation"`
}
type planDeficitView struct {
	Clinic int64   `json:"clinic_id"`
	Kind   string  `json:"component_kind"`
	Group  string  `json:"blood_group"`
	Volume float64 `json:"uncovered_ml"`
	Reason string  `json:"reason"`
	Detail string  `json:"detail"`
}
type planProtectedView struct {
	Clinic    int64  `json:"clinic_id"`
	Kind      string `json:"component_kind"`
	Units     int64  `json:"protected_units"`
	Remaining int64  `json:"remaining_units"`
}
type planView struct {
	Obligations          []planObligationView `json:"obligations"`
	NormVersion          int64                `json:"norm_version"`
	StockSynced          time.Time            `json:"stock_synced_at"`
	ForecastID           *uuid.UUID           `json:"emergency_forecast_id"`
	ScenarioVersion      string               `json:"scenario_version"`
	DeliveryVersion      int64                `json:"delivery_version"`
	ObligationsWatermark int64                `json:"obligations_watermark"`
	RefdataVersion       int                  `json:"refdata_version"`
	AlgorithmVersion     string               `json:"algorithm_version"`
	Targets              []planTargetView     `json:"targets"`
	ID                   uuid.UUID            `json:"replenishment_plan_id"`
	Demand               uuid.UUID            `json:"demand_estimate_id"`
	DemandVersion        int                  `json:"demand_version_no"`
	Watermark            int64                `json:"stock_watermark"`
	Created              time.Time            `json:"created_at"`
	Coverage             float64              `json:"coverage_ratio"`
	Elapsed              int64                `json:"elapsed_ms"`
	Notice               string               `json:"notice"`
	Proposals            []planProposalView   `json:"proposals"`
	Deficits             []planDeficitView    `json:"deficits"`
	Protected            []planProtectedView  `json:"protected"`
}

type planObligationView struct {
	ID      uuid.UUID `json:"obligation_id"`
	Source  int64     `json:"source_clinic_id"`
	Target  int64     `json:"target_clinic_id"`
	Kind    string    `json:"component_kind"`
	Status  string    `json:"status"`
	Units   int       `json:"confirmed_units"`
	Blocked bool      `json:"clinical_use_blocked"`
}
type planTargetView struct {
	Clinic     int64  `json:"clinic_id"`
	Kind       string `json:"component_kind"`
	Group      string `json:"blood_group"`
	Norm       int64  `json:"norm_ml"`
	Background int64  `json:"background_ml"`
	Emergency  int64  `json:"emergency_ml"`
	Target     int64  `json:"target_ml"`
	Stock      int64  `json:"stock_ml"`
	Inbound    int64  `json:"inbound_ml"`
	Proposed   int64  `json:"proposed_ml"`
	Uncovered  int64  `json:"uncovered_ml"`
	Rough      bool   `json:"is_rough_estimate"`
}
type planningScreen struct {
	CanViewTasks bool
	Forecasts    []forecastView
	Plans        []planView
	Plan         planView
	Key          uuid.UUID
	CanBuild     bool
}

type forecastView struct {
	ID      uuid.UUID          `json:"emergency_forecast_id"`
	Version string             `json:"scenario_version"`
	Rough   bool               `json:"is_rough_estimate"`
	Start   time.Time          `json:"period_start"`
	End     time.Time          `json:"period_end"`
	Lines   []forecastLineView `json:"lines"`
}
type forecastLineView struct {
	ID     uuid.UUID `json:"line_id"`
	Clinic string    `json:"clinic_code"`
	Kind   string    `json:"component_kind"`
	Group  string    `json:"blood_group"`
	Volume int64     `json:"volume_ml"`
}
type forecastScreen struct {
	Forecast  forecastView
	CanAdjust bool
}

type deficitRecordView struct {
	Type   string    `json:"record_type"`
	Origin uuid.UUID `json:"origin_id"`
	Clinic int64     `json:"clinic_id"`
	Kind   string    `json:"component_kind"`
	Group  string    `json:"blood_group"`
	ML     int64     `json:"uncovered_ml"`
	Units  int       `json:"uncovered_units"`
	Reason string    `json:"reason"`
	At     time.Time `json:"created_at"`
}
type deficitsScreen struct {
	Records              []deficitRecordView
	Clinic, Type, Reason string
}

func (s *Server) deficitsPage(w http.ResponseWriter, r *http.Request) {
	q := url.Values{}
	for _, name := range []string{"clinic", "record_type", "reason"} {
		if v := r.URL.Query().Get(name); v != "" {
			q.Set(name, v)
		}
	}
	data := deficitsScreen{Clinic: q.Get("clinic"), Type: q.Get("record_type"), Reason: q.Get("reason")}
	if err := s.planning.GetJSON(r.Context(), "/deficits?"+q.Encode(), &data.Records); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	s.renderScreen(w, r, 200, "deficits.html", "Реестр дефицита", data)
}

func (s *Server) registerPlanning(mux *http.ServeMux) {
	mux.Handle("GET /deficits", s.guard.Require(auth.OpViewPlan)(http.HandlerFunc(s.deficitsPage)))
	mux.Handle("GET /forecasts/{id}", s.guard.Require(auth.OpViewPlan)(http.HandlerFunc(s.forecastDetail)))
	mux.Handle("POST /forecasts/{id}/lines/{lineId}", s.guard.Require(auth.OpAdjustForecast)(http.HandlerFunc(s.adjustForecast)))
	mux.Handle("GET /plans", s.guard.Require(auth.OpViewPlan)(http.HandlerFunc(s.planList)))
	mux.Handle("POST /plans", s.guard.Require(auth.OpBuildPlan)(http.HandlerFunc(s.buildPlan)))
	mux.Handle("GET /plans/{id}", s.guard.Require(auth.OpViewPlan)(http.HandlerFunc(s.planDetail)))
}
func (s *Server) planList(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	data := planningScreen{Key: uuid.New(), CanBuild: auth.Allowed(auth.OpBuildPlan, p.Roles)}
	if err := s.planning.GetJSON(r.Context(), "/plans", &data.Plans); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	if err := s.planning.GetJSON(r.Context(), "/forecasts", &data.Forecasts); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	s.renderScreen(w, r, 200, "plans.html", "План пополнения", data)
}

func (s *Server) forecastDetail(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.renderError(w, r, 400, "Неверный прогноз", "Проверьте адрес.")
		return
	}
	p, _ := auth.FromContext(r.Context())
	data := forecastScreen{CanAdjust: auth.Allowed(auth.OpAdjustForecast, p.Roles)}
	if err := s.planning.GetJSON(r.Context(), "/forecasts/"+id.String(), &data.Forecast); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	s.renderScreen(w, r, 200, "forecast.html", "Прогноз ЧС", data)
}
func (s *Server) adjustForecast(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.renderError(w, r, 400, "Неверный прогноз", "Проверьте адрес.")
		return
	}
	line, err := uuid.Parse(r.PathValue("lineId"))
	if err != nil {
		s.renderError(w, r, 400, "Неверная строка", "Обновите прогноз.")
		return
	}
	volume, err := strconv.ParseInt(r.FormValue("volume_ml"), 10, 64)
	if err != nil {
		s.renderError(w, r, 400, "Неверный объём", "Введите целое число миллилитров.")
		return
	}
	var out struct {
		ID uuid.UUID `json:"emergency_forecast_id"`
	}
	if err := s.planning.PatchJSON(r.Context(), "/forecasts/"+id.String()+"/lines/"+line.String(), map[string]any{"volume_ml": volume, "clinic_code": r.FormValue("clinic_code"), "reason": r.FormValue("reason")}, &out); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	http.Redirect(w, r, "/forecasts/"+out.ID.String(), http.StatusSeeOther)
}
func (s *Server) planDetail(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.renderError(w, r, 400, "Неверный план", "Проверьте адрес.")
		return
	}
	p, _ := auth.FromContext(r.Context())
	data := planningScreen{CanViewTasks: auth.Allowed(auth.OpViewObligation, p.Roles)}
	if err := s.planning.GetJSON(r.Context(), "/plans/"+id.String(), &data.Plan); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	s.renderScreen(w, r, 200, "plan.html", "Результат планирования", data)
}
func (s *Server) buildPlan(w http.ResponseWriter, r *http.Request) {
	key, err := uuid.Parse(r.FormValue("idempotency_key"))
	if err != nil {
		s.renderError(w, r, 400, "Неверный запрос", "Обновите форму построения плана.")
		return
	}
	input := map[string]any{"idempotency_key": key}
	if value := r.FormValue("emergency_forecast_id"); value != "" {
		id, err := uuid.Parse(value)
		if err != nil {
			s.renderError(w, r, 400, "Неверный прогноз", "Укажите идентификатор квитанции C02.")
			return
		}
		input["emergency_forecast_id"] = id
	}
	var plan planView
	if err := s.planning.PostJSON(r.Context(), "/plans", input, &plan); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	http.Redirect(w, r, "/plans/"+plan.ID.String(), http.StatusSeeOther)
}
