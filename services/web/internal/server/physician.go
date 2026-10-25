package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/svcclient"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/ui"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
)

type requestVariantView struct {
	ID             uuid.UUID `json:"variant_offer_id"`
	Rank           int       `json:"rank_no"`
	Source         int64     `json:"source_clinic_id"`
	Local          bool      `json:"is_local"`
	Kind           string    `json:"component_kind"`
	Group          string    `json:"blood_group"`
	Available      int       `json:"available_units"`
	Offered        int       `json:"offered_units"`
	Minutes        int       `json:"delivery_minutes"`
	Delivery       string    `json:"delivery_status"`
	DeficitStock   bool      `json:"is_deficit_stock_used"`
	ProtectedKnown bool      `json:"protected_known"`
	Explanation    string    `json:"explanation"`
	Rule           string    `json:"rule_code"`
	RuleVersion    int64     `json:"rule_version"`
}
type clinicalRequestView struct {
	ClinicalUseBlocked bool                    `json:"clinical_use_blocked"`
	Obligations        []requestObligationView `json:"obligations"`
	PatientStatus      string                  `json:"patient_allocation_status"`
	ID                 uuid.UUID               `json:"clinical_request_id"`
	Clinic             int64                   `json:"clinic_id"`
	Status             string                  `json:"status"`
	Kind               string                  `json:"component_kind"`
	Requested          int                     `json:"requested_units"`
	Urgency            string                  `json:"urgency"`
	Group              string                  `json:"blood_group"`
	Created            time.Time               `json:"created_at"`
	Watermark          int64                   `json:"stock_watermark"`
	Synced             time.Time               `json:"stock_synced_at"`
	Variants           []requestVariantView    `json:"variants"`
	Notes              []string                `json:"notes"`
}
type requestObligationView struct {
	ID      uuid.UUID `json:"obligation_id"`
	Source  int64     `json:"source_clinic_id"`
	Status  string    `json:"status"`
	Units   int       `json:"units"`
	Blocked bool      `json:"clinical_use_blocked"`
}

func (r clinicalRequestView) PatientDeliveryLabel() string {
	return patientDeliveryLabel(r.PatientStatus)
}

type physicianScreen struct {
	Requests                                     []clinicalRequestView
	Request                                      clinicalRequestView
	Key                                          uuid.UUID
	CanCreate, CanSelect, CanCancel, CanReadCase bool
}
type patientCaseScreen struct {
	RequestID uuid.UUID
	Plain     string
}

func (s *Server) registerPhysician(mux *http.ServeMux) {
	mux.Handle("GET /rules", s.guard.Require(auth.OpViewRules)(http.HandlerFunc(s.rulesPage)))
	mux.Handle("POST /rules/{code}/versions", s.guard.Require(auth.OpManageRules)(http.HandlerFunc(s.addRuleVersion)))
	mux.Handle("GET /requests", s.guard.Require(auth.OpViewRequests)(http.HandlerFunc(s.requestList)))
	mux.Handle("GET /requests/new", s.guard.Require(auth.OpCreateRequest)(http.HandlerFunc(s.newRequest)))
	mux.Handle("POST /requests", s.guard.Require(auth.OpCreateRequest)(http.HandlerFunc(s.createClinicalRequest)))
	mux.Handle("GET /requests/{id}", s.guard.Require(auth.OpViewRequests)(http.HandlerFunc(s.requestDetail)))
	mux.Handle("POST /requests/{id}/select", s.guard.Require(auth.OpSelectVariant)(http.HandlerFunc(s.selectRequestVariant)))
	mux.Handle("POST /requests/{id}/cancel", s.guard.Require(auth.OpCancelRequest)(http.HandlerFunc(s.cancelClinicalRequest)))
	mux.Handle("GET /requests/{id}/patient-case", s.guard.Require(auth.OpReadPatientCase)(http.HandlerFunc(s.patientCase)))
}
func (s *Server) requestList(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	data := physicianScreen{CanCreate: auth.Allowed(auth.OpCreateRequest, p.Roles)}
	if err := s.matching.GetJSON(r.Context(), "/requests", &data.Requests); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	s.renderScreen(w, r, 200, "requests.html", "Заявки врача", data)
}
func (s *Server) newRequest(w http.ResponseWriter, r *http.Request) {
	s.renderScreen(w, r, 200, "request-new.html", "Новая заявка", physicianScreen{Key: uuid.New()})
}
func (s *Server) createClinicalRequest(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.FormValue("idempotency_key"))
	if err != nil {
		s.renderError(w, r, 400, "Форма устарела", "Откройте новую заявку.")
		return
	}
	quantity, err := strconv.Atoi(r.FormValue("requested_units"))
	if err != nil || quantity < 1 || quantity > maxRequestUnits {
		s.renderError(w, r, 400, "Неверное количество", fmt.Sprintf("Введите целое число единиц от 1 до %d.", maxRequestUnits))
		return
	}
	constraints := []map[string]string{}
	for _, code := range []string{"NO_GROUP_SUBSTITUTION", "IRRADIATED"} {
		if r.FormValue(code) == "true" {
			constraints = append(constraints, map[string]string{"code": code, "value": ""})
		}
	}
	if value := r.FormValue("min_remaining_hours"); value != "" {
		constraints = append(constraints, map[string]string{"code": "MIN_REMAINING_HOURS", "value": value})
	}
	input := map[string]any{"idempotency_key": id, "patient_case_no": r.FormValue("patient_case_no"), "component_kind": r.FormValue("component_kind"), "requested_units": quantity, "urgency": r.FormValue("urgency"), "blood_group": r.FormValue("blood_group"), "phenotype": r.FormValue("phenotype"), "constraints": constraints}
	var out clinicalRequestView
	if err := s.matching.PostJSON(r.Context(), "/requests", input, &out); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	http.Redirect(w, r, "/requests/"+out.ID.String(), http.StatusSeeOther)
}
func (s *Server) requestDetail(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.renderError(w, r, 400, "Неверная заявка", "Проверьте адрес.")
		return
	}
	p, _ := auth.FromContext(r.Context())
	data := physicianScreen{Key: uuid.NewSHA1(id, []byte("doctor-choice")), CanReadCase: auth.Allowed(auth.OpReadPatientCase, p.Roles)}
	if err := s.matching.GetJSON(r.Context(), "/requests/"+id.String(), &data.Request); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	data.CanSelect = data.Request.Status == "OFFERS_READY" && auth.Allowed(auth.OpSelectVariant, p.Roles)
	data.CanCancel = data.Request.Status != "CANCELLED" && data.Request.Status != "CANCELLATION_REQUESTED" && data.Request.Status != "CANCELLATION_REJECTED" && data.Request.Status != "DISPATCHED" && data.Request.Status != "DELIVERED" && auth.Allowed(auth.OpCancelRequest, p.Roles)
	s.renderScreen(w, r, 200, "request.html", "Заявка и варианты", data)
}
func (s *Server) selectRequestVariant(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.renderError(w, r, 400, "Неверная заявка", "Проверьте адрес.")
		return
	}
	variant, err := uuid.Parse(r.FormValue("variant_offer_id"))
	if err != nil {
		s.renderError(w, r, 400, "Неверный вариант", "Обновите список.")
		return
	}
	key, err := uuid.Parse(r.FormValue("choice_id"))
	if err != nil {
		s.renderError(w, r, 400, "Форма устарела", "Обновите список.")
		return
	}
	var out clinicalRequestView
	if err := s.matching.PostJSON(r.Context(), "/requests/"+id.String()+"/select", map[string]any{"variant_offer_id": variant, "choice_id": key}, &out); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	http.Redirect(w, r, "/requests/"+id.String(), http.StatusSeeOther) // #nosec G710 -- id разобран uuid.Parse и сериализован канонически; префикс фиксирован.
}

// maxRequestUnits — предел количества в одной заявке врача: больше требует решения не одной заявки, а плана.
const maxRequestUnits = 50

func (s *Server) cancelClinicalRequest(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.renderError(w, r, 400, "Неверная заявка", "Проверьте адрес.")
		return
	}
	if err := s.matching.PostJSON(r.Context(), "/requests/"+id.String()+"/cancel", struct{}{}, nil); err != nil {
		var statusErr *svcclient.StatusError
		if errors.As(err, &statusErr) && statusErr.Code == http.StatusConflict {
			s.renderError(w, r, http.StatusConflict, "Отмена невозможна", "Заявка изменилась или поставка уже отправлена либо доставлена. Откройте заявку и проверьте задания.")
			return
		}
		s.demandFailure(w, r, err)
		return
	}
	ui.SetFlash(w, s.cfg.CookieSecure, ui.Flash{Tone: ui.ToneSuccess, Text: "Отмена заявки запрошена. Результат придёт от источников."})
	http.Redirect(w, r, "/requests/"+id.String(), http.StatusSeeOther) // #nosec G710 -- id разобран uuid.Parse и сериализован канонически; префикс фиксирован.
}
func (s *Server) patientCase(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.renderError(w, r, 400, "Неверная заявка", "Проверьте адрес.")
		return
	}
	var out struct {
		Plain string `json:"patient_case_no"`
	}
	if err := s.matching.GetJSON(r.Context(), "/requests/"+id.String()+"/patient-case", &out); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	s.renderScreen(w, r, 200, "patient-case.html", "Номер случая пациента", patientCaseScreen{RequestID: id, Plain: out.Plain})
}
