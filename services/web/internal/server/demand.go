package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/svcclient"
)

type demandFileView struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"file_name"`
	Kind     string    `json:"file_kind"`
	Status   string    `json:"check_status"`
	Rejected int       `json:"rejected_rows"`
	Accepted int       `json:"accepted_rows"`
}
type demandHistoryView struct {
	ID     uuid.UUID `json:"id"`
	Kind   string    `json:"component_kind_code"`
	Start  string    `json:"period_start"`
	End    string    `json:"period_end"`
	Volume int64     `json:"consumed_volume_ml"`
}
type demandShareView struct {
	ID      uuid.UUID `json:"id"`
	Version int64     `json:"version_no"`
}
type demandEstimateLine struct {
	Clinic int64   `json:"clinic_id"`
	Kind   string  `json:"component_kind"`
	Group  string  `json:"blood_group"`
	Volume float64 `json:"demand_volume_ml"`
}
type demandEstimateView struct {
	RejectionReason string               `json:"rejection_reason"`
	ID              uuid.UUID            `json:"demand_estimate_id"`
	Version         int                  `json:"version_no"`
	Status          string               `json:"status"`
	Start           string               `json:"period_start"`
	End             string               `json:"period_end"`
	Profile         uuid.UUID            `json:"profile_file_id"`
	Shares          uuid.UUID            `json:"clinic_share_version_id"`
	History         []uuid.UUID          `json:"history_ids"`
	Lines           []demandEstimateLine `json:"lines"`
	Notice          string               `json:"notice"`
}
type demandRowError struct {
	Row    int    `json:"row_no"`
	Reason string `json:"reason"`
}
type demandScreen struct {
	File      demandFileView
	Files     []demandFileView
	History   []demandHistoryView
	Shares    []demandShareView
	Estimates []demandEstimateView
	Estimate  demandEstimateView
	CanWrite  bool
	Message   string
	Errors    []demandRowError
}

func (s *Server) registerDemand(mux *http.ServeMux) {
	mux.Handle("GET /demand/templates/{kind}", s.guard.Require(auth.OpUploadStatistics)(http.HandlerFunc(s.demandCSVTemplate)))
	mux.Handle("GET /demand/files/{id}", s.guard.Require(auth.OpUploadStatistics)(http.HandlerFunc(s.demandFile)))
	mux.Handle("POST /demand/estimates/{id}/reject", s.guard.Require(auth.OpPublishEstimate)(http.HandlerFunc(s.rejectDemand)))
	mux.Handle("GET /demand", s.guard.Require(auth.OpViewDemand)(http.HandlerFunc(s.demandPage)))
	mux.Handle("POST /demand/files", s.guard.Require(auth.OpUploadStatistics)(http.HandlerFunc(s.uploadDemandFile)))
	mux.Handle("POST /demand/estimates", s.guard.Require(auth.OpPublishEstimate)(http.HandlerFunc(s.calculateDemand)))
	mux.Handle("GET /demand/estimates/{id}", s.guard.Require(auth.OpViewDemand)(http.HandlerFunc(s.demandEstimate)))
	mux.Handle("POST /demand/estimates/{id}/publish", s.guard.Require(auth.OpPublishEstimate)(http.HandlerFunc(s.publishDemand)))
}

func (s *Server) demandFailure(w http.ResponseWriter, r *http.Request, err error) {
	var response *svcclient.StatusError
	if errors.As(err, &response) && (response.Code == 400 || response.Code == 409 || response.Code == 413 || response.Code == 422) {
		s.renderError(w, r, response.Code, "Операция не выполнена", response.Problem.Detail)
		return
	}
	s.serviceFailure(w, r, err)
}

func (s *Server) demandPage(w http.ResponseWriter, r *http.Request) { s.showDemand(w, r, "", nil) }

func (s *Server) showDemand(w http.ResponseWriter, r *http.Request, message string, rowErrors []demandRowError) {
	p, _ := auth.FromContext(r.Context())
	data := demandScreen{CanWrite: auth.Allowed(auth.OpPublishEstimate, p.Roles), Message: message, Errors: rowErrors}
	if err := s.demand.GetJSON(r.Context(), "/estimates", &data.Estimates); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	if data.CanWrite {
		var files struct {
			Items []demandFileView `json:"items"`
		}
		var history struct {
			Items []demandHistoryView `json:"items"`
		}
		var shares struct {
			Items []demandShareView `json:"items"`
		}
		for _, call := range []struct {
			path string
			out  any
		}{{"/files", &files}, {"/history", &history}, {"/shares/versions", &shares}} {
			if err := s.demand.GetJSON(r.Context(), call.path, call.out); err != nil {
				s.demandFailure(w, r, err)
				return
			}
		}
		data.Files, data.History, data.Shares = files.Items, history.Items, shares.Items
	}
	s.renderScreen(w, r, 200, "demand.html", "Потребность территории", data)
}

func (s *Server) uploadDemandFile(w http.ResponseWriter, r *http.Request) {
	file, header, err := r.FormFile("file")
	if err != nil {
		s.renderError(w, r, 400, "Файл не выбран", "Выберите CSV не больше 5 МиБ.")
		return
	}
	defer func() { _ = file.Close() }()
	if header.Size > 5<<20 {
		s.renderError(w, r, 413, "Файл слишком большой", "Разделите файл: предел 5 МиБ.")
		return
	}
	kind := r.PostFormValue("file_kind")
	if kind != "DISTRICT_PROFILE" && kind != "CONSUMPTION_HISTORY" && kind != "CLINIC_SHARE" {
		s.renderError(w, r, 400, "Неверный вид файла", "Выберите вид из списка.")
		return
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("file_kind", kind); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	part, err := mw.CreateFormFile("file", header.Filename)
	if err != nil {
		s.demandFailure(w, r, err)
		return
	}
	if _, err := io.Copy(part, io.LimitReader(file, (5<<20)+1)); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	if err := mw.Close(); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	resp, err := s.demand.Do(r.Context(), http.MethodPost, "/files", &buf, mw.FormDataContentType())
	if err != nil {
		s.demandFailure(w, r, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 201 {
		var problem struct {
			Detail string `json:"detail"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&problem)
		s.renderError(w, r, resp.StatusCode, "Файл не принят", problem.Detail)
		return
	}
	var result struct {
		File   demandFileView   `json:"file"`
		Errors []demandRowError `json:"row_errors"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	s.showDemand(w, r, fmt.Sprintf("Файл проверен: %s. Принято строк: %d, отклонено: %d.", result.File.Status, result.File.Accepted, result.File.Rejected), result.Errors)
}

func (s *Server) calculateDemand(w http.ResponseWriter, r *http.Request) {
	profile, e1 := uuid.Parse(r.PostFormValue("profile_file_id"))
	shares, e2 := uuid.Parse(r.PostFormValue("clinic_share_version_id"))
	start, e3 := time.Parse(time.DateOnly, r.PostFormValue("period_start"))
	end, e4 := time.Parse(time.DateOnly, r.PostFormValue("period_end"))
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || end.Before(start) {
		s.renderError(w, r, 400, "Не выбраны входы", "Укажите профиль, доли и период оценки.")
		return
	}
	history := r.PostForm["history_ids"]
	if len(history) == 0 || len(history) > 10000 {
		s.renderError(w, r, 400, "Не выбрана история", "Отметьте периоды расхода для расчёта.")
		return
	}
	for _, id := range history {
		if _, err := uuid.Parse(id); err != nil {
			s.renderError(w, r, 400, "Неверная история", "Обновите страницу.")
			return
		}
	}
	body := map[string]any{"profile_file_id": profile, "clinic_share_version_id": shares, "period_start": start.Format(time.DateOnly), "period_end": end.Format(time.DateOnly), "history_ids": history}
	var result demandEstimateView
	if err := s.demand.PostJSON(r.Context(), "/estimates", body, &result); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	http.Redirect(w, r, "/demand/estimates/"+result.ID.String(), http.StatusSeeOther)
}

func (s *Server) demandEstimate(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.renderError(w, r, 400, "Неверная версия", "Откройте оценку из списка.")
		return
	}
	var estimate demandEstimateView
	if err := s.demand.GetJSON(r.Context(), "/estimates/"+id.String(), &estimate); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	p, _ := auth.FromContext(r.Context())
	s.renderScreen(w, r, 200, "demand-estimate.html", "Версия оценки потребности", demandScreen{Estimate: estimate, CanWrite: auth.Allowed(auth.OpPublishEstimate, p.Roles) && estimate.Status == "CALCULATED"})
}

func (s *Server) publishDemand(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.renderError(w, r, 400, "Неверная версия", "Откройте оценку из списка.")
		return
	}
	if err := s.demand.PostJSON(r.Context(), "/estimates/"+id.String()+"/publish", struct{}{}, nil); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	http.Redirect(w, r, "/demand/estimates/"+id.String(), http.StatusSeeOther) //nolint:gosec // id разобран uuid.Parse; адрес состоит из фиксированного локального пути и UUID
}

func (s *Server) rejectDemand(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.renderError(w, r, 400, "Неверная версия", "Откройте оценку из списка.")
		return
	}
	if err := s.demand.PostJSON(r.Context(), "/estimates/"+id.String()+"/reject", map[string]string{"reason": r.FormValue("reason")}, nil); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	http.Redirect(w, r, "/demand/estimates/"+id.String(), http.StatusSeeOther) //nolint:gosec // Локальный путь с UUID после uuid.Parse.
}
func (s *Server) demandFile(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.renderError(w, r, 400, "Неверный файл", "Откройте файл из истории.")
		return
	}
	var data demandScreen
	if err := s.demand.GetJSON(r.Context(), "/files/"+id.String(), &data.File); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	var rows struct {
		Items []demandRowError `json:"items"`
	}
	if err := s.demand.GetJSON(r.Context(), "/files/"+id.String()+"/errors", &rows); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	data.Errors = rows.Items
	s.renderScreen(w, r, 200, "demand-file.html", "История проверки CSV", data)
}

func (f demandFileView) Profile() bool { return f.Kind == "DISTRICT_PROFILE" && f.Status != "REJECTED" }
func (f demandFileView) Label() string { return strings.TrimSpace(f.Name) + " (" + f.Status + ")" }
