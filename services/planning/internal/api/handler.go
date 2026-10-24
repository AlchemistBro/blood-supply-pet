package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/int02"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/planning/internal/api/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/planning/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/planning/internal/domain"
)

var Operations = map[string]auth.Operation{
	"POST /int/forecast":                   auth.OpReceiveForecast,
	"GET /plans":                           auth.OpViewPlan,
	"POST /plans":                          auth.OpBuildPlan,
	"GET /plans/{id}":                      auth.OpViewPlan,
	"GET /forecasts":                       auth.OpViewPlan,
	"GET /forecasts/{id}":                  auth.OpViewPlan,
	"PATCH /forecasts/{id}/lines/{lineId}": auth.OpAdjustForecast,
	"GET /deficits":                        auth.OpViewPlan,
}

type Handler struct{ svc *app.Service }

func (h *Handler) ListDeficits(ctx context.Context, req gen.ListDeficitsRequestObject) (gen.ListDeficitsResponseObject, error) {
	clinic := int64(0)
	kind, reason := "", ""
	if req.Params.Clinic != nil {
		clinic = *req.Params.Clinic
	}
	if req.Params.RecordType != nil {
		kind = *req.Params.RecordType
	}
	if req.Params.Reason != nil {
		reason = *req.Params.Reason
	}
	items, err := h.svc.Deficits(ctx, clinic, kind, reason)
	if err != nil {
		return nil, err
	}
	out := make(gen.ListDeficits200JSONResponse, 0, len(items))
	for _, n := range items {
		out = append(out, gen.DeficitRecord{DeficitRecordId: n.ID, RecordType: n.Type, OriginId: n.OriginID, ClinicId: n.ClinicID, ComponentKind: n.Kind, BloodGroup: n.Group, UncoveredMl: n.ML, UncoveredUnits: n.Units, Reason: n.Reason, CreatedAt: n.At})
	}
	return out, nil
}

func forecastView(id uuid.UUID, f int02.EmergencyForecast) gen.Forecast {
	out := gen.Forecast{EmergencyForecastId: id, ScenarioVersion: f.ScenarioVersion, PeriodStart: f.PeriodStart, PeriodEnd: f.PeriodEnd, Lines: []gen.ForecastLine{}}
	for i, line := range f.Lines {
		out.IsRoughEstimate = out.IsRoughEstimate || line.ClinicCode == ""
		out.Lines = append(out.Lines, gen.ForecastLine{LineId: uuid.NewSHA1(id, []byte(strconv.Itoa(i))), ClinicCode: line.ClinicCode, ComponentKind: line.ComponentKind, BloodGroup: line.BloodGroup, VolumeMl: line.VolumeML})
	}
	return out
}
func (h *Handler) ListForecasts(ctx context.Context, _ gen.ListForecastsRequestObject) (gen.ListForecastsResponseObject, error) {
	items, err := h.svc.Forecasts(ctx)
	if err != nil {
		return nil, err
	}
	out := make(gen.ListForecasts200JSONResponse, 0, len(items))
	for _, item := range items {
		out = append(out, forecastView(item.ID, item.Forecast))
	}
	return out, nil
}
func (h *Handler) GetForecast(ctx context.Context, req gen.GetForecastRequestObject) (gen.GetForecastResponseObject, error) {
	f, err := h.svc.Forecast(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return gen.GetForecast200JSONResponse(forecastView(req.Id, f)), nil
}
func (h *Handler) AdjustForecast(ctx context.Context, req gen.AdjustForecastRequestObject) (gen.AdjustForecastResponseObject, error) {
	if req.Body == nil {
		return nil, app.ErrInvalid
	}
	p, _ := auth.FromContext(ctx)
	id, err := h.svc.AdjustForecast(ctx, req.Id, req.LineId, req.Body.VolumeMl, req.Body.ClinicCode, req.Body.Reason, p.Username)
	if err != nil {
		return nil, err
	}
	return gen.AdjustForecast200JSONResponse{EmergencyForecastId: id}, nil
}

func Mount(mux *http.ServeMux, svc *app.Service, onDenied auth.Denied) {
	h := &Handler{svc: svc}
	strict := gen.NewStrictHandlerWithOptions(h, nil, gen.StrictHTTPServerOptions{RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
		httpx.WriteProblem(w, r, 400, "Неверный запрос", "Проверьте параметры и формат JSON.")
	}, ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
		code := 500
		message := "Запрос не выполнен. Повторите позже."
		switch {
		case errors.Is(err, app.ErrInvalid), errors.Is(err, domain.ErrInvalidInput):
			code = 422
			message = err.Error()
		case errors.Is(err, app.ErrNotFound):
			code = 404
			message = "Прогноз или план не найден."
		case errors.Is(err, app.ErrConflict):
			code = 409
			message = "Изменённый сценарий должен иметь новую версию."
		}
		httpx.WriteProblem(w, r, code, "Планирование не выполнено", message)
	}})
	gen.HandlerWithOptions(strict, gen.StdHTTPServerOptions{BaseRouter: mux, Middlewares: []gen.MiddlewareFunc{func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			op, ok := Operations[r.Pattern]
			if !ok {
				httpx.WriteProblem(w, r, 403, "Недостаточно прав", "Операция не описана.")
				return
			}
			auth.Require(op, onDenied)(next).ServeHTTP(w, r)
		})
	}}})
}

func (h *Handler) AcceptForecast(ctx context.Context, req gen.AcceptForecastRequestObject) (gen.AcceptForecastResponseObject, error) {
	if req.Body == nil {
		return nil, app.ErrInvalid
	}
	raw, err := json.Marshal(req.Body)
	if err != nil {
		return nil, app.ErrInvalid
	}
	// Пересборка через узкий контракт удаляет лишние поля на каждом уровне до сохранения (NFR010).
	var forecast int02.EmergencyForecast
	if err := json.Unmarshal(raw, &forecast); err != nil {
		return nil, app.ErrInvalid
	}
	p, _ := auth.FromContext(ctx)
	id, err := h.svc.AcceptForecast(ctx, forecast, p.Username)
	if err != nil {
		return nil, err
	}
	return gen.AcceptForecast201JSONResponse{EmergencyForecastId: id}, nil
}

func (h *Handler) ListPlans(ctx context.Context, _ gen.ListPlansRequestObject) (gen.ListPlansResponseObject, error) {
	items, err := h.svc.Plans(ctx)
	if err != nil {
		return nil, err
	}
	out := make(gen.ListPlans200JSONResponse, 0, len(items))
	for _, n := range items {
		out = append(out, planView(n))
	}
	return out, nil
}
func (h *Handler) GetPlan(ctx context.Context, req gen.GetPlanRequestObject) (gen.GetPlanResponseObject, error) {
	n, err := h.svc.Plan(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return gen.GetPlan200JSONResponse(planView(n)), nil
}
func (h *Handler) BuildPlan(ctx context.Context, req gen.BuildPlanRequestObject) (gen.BuildPlanResponseObject, error) {
	if req.Body == nil {
		return nil, app.ErrInvalid
	}
	p, _ := auth.FromContext(ctx)
	n, err := h.svc.Build(ctx, req.Body.IdempotencyKey, req.Body.EmergencyForecastId, p.Username, p.PrimaryRole())
	if err != nil {
		return nil, err
	}
	return gen.BuildPlan200JSONResponse(planView(n)), nil
}

func planView(n app.PlanRecord) gen.Plan {
	p := n.Plan
	out := gen.Plan{ReplenishmentPlanId: p.PlanID, DemandEstimateId: p.Inputs.DemandEstimateID, DemandVersionNo: p.Inputs.DemandVersionNo, StockWatermark: p.Inputs.StockWatermark, CoverageRatio: p.CoverageRatio, CreatedAt: p.CreatedAt, ElapsedMs: n.ElapsedMS, Notice: "Демо-правила. Передачи требуют подтверждения оператора клиники-источника.", Proposals: make([]gen.Proposal, 0, len(p.Proposals)), Deficits: make([]gen.Deficit, 0, len(p.Deficits)), Protected: make([]gen.Protected, 0, len(p.Protected))}
	out.NormVersion = p.Inputs.NormVersion
	out.StockSyncedAt = p.Inputs.StockSyncedAt
	out.ScenarioVersion = p.Inputs.ScenarioVersion
	out.DeliveryVersion = p.Inputs.DeliveryEstimatesVersion
	out.ObligationsWatermark = p.Inputs.ObligationsWatermark
	out.RefdataVersion = p.Inputs.RefdataVersion
	out.AlgorithmVersion = p.Inputs.AlgorithmVersion
	if p.Inputs.ForecastID != uuid.Nil {
		out.EmergencyForecastId = &p.Inputs.ForecastID
	}
	out.Targets = make([]gen.Target, 0, len(p.Targets))
	for _, v := range p.Targets {
		out.Targets = append(out.Targets, gen.Target{ClinicId: v.ClinicID, ComponentKind: v.Kind, BloodGroup: v.Group, NormMl: v.NormML, BackgroundMl: v.BackgroundML, EmergencyMl: v.EmergencyML, TargetMl: v.TargetML, StockMl: v.StockML, InboundMl: v.InboundML, ProposedMl: v.ProposedML, UncoveredMl: v.UncoveredML, IsRoughEstimate: v.IsRoughEstimate})
	}
	for _, v := range p.Proposals {
		out.Proposals = append(out.Proposals, gen.Proposal{ProposalId: v.ProposalID, SourceClinicId: v.SourceClinicID, TargetClinicId: v.TargetClinicID, ComponentKind: v.Kind, BloodGroup: v.Group, Units: v.Units, VolumeMl: v.VolumeML, DeliveryMinutes: v.DeliveryMinutes, Explanation: v.Explanation.Text()})
	}
	for _, v := range p.Deficits {
		out.Deficits = append(out.Deficits, gen.Deficit{ClinicId: v.ClinicID, ComponentKind: v.Kind, BloodGroup: v.Group, UncoveredMl: v.UncoveredML, Reason: v.Reason, Detail: v.Detail})
	}
	for _, v := range p.Protected {
		out.Protected = append(out.Protected, gen.Protected{ClinicId: v.ClinicID, ComponentKind: v.Kind, ProtectedUnits: v.VolumeUnits, RemainingUnits: v.RemainingUnits, ProtectedMl: v.VolumeML, RemainingMl: v.RemainingML})
	}
	return out
}
