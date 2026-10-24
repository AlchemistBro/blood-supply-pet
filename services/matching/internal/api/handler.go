package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/api/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/domain"
)

var Operations = map[string]auth.Operation{
	"GET /requests": auth.OpViewRequests, "POST /requests": auth.OpCreateRequest,
	"GET /requests/{id}": auth.OpViewRequests, "GET /requests/{id}/variants": auth.OpViewRequests,
	"GET /requests/{id}/patient-case": auth.OpReadPatientCase,
	"POST /requests/{id}/select":      auth.OpSelectVariant, "POST /requests/{id}/cancel": auth.OpCancelRequest,
	"GET /rules": auth.OpViewRules, "POST /rules/{code}/versions": auth.OpManageRules,
}

type Handler struct{ svc *app.Service }

func Actor(ctx context.Context) app.Actor {
	p, _ := auth.FromContext(ctx)
	out := app.Actor{User: p.Subject}
	if out.User == "" {
		out.User = p.Username
	}
	if p.ClinicID != nil {
		out.ClinicID = *p.ClinicID
	}
	for _, role := range p.Roles {
		out.Roles = append(out.Roles, string(role))
	}
	return out
}

func Mount(mux *http.ServeMux, svc *app.Service, onDenied auth.Denied) {
	h := &Handler{svc: svc}
	strict := gen.NewStrictHandlerWithOptions(h, nil, gen.StrictHTTPServerOptions{RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
		httpx.WriteProblem(w, r, 400, "Неверный запрос", "Проверьте формат и параметры.")
	}, ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
		code, message := 500, "Сервис подбора временно недоступен."
		switch {
		case errors.Is(err, app.ErrForbidden):
			code, message = 403, "Нет доступа к заявке или номеру случая."
		case errors.Is(err, app.ErrNotFound):
			code, message = 404, "Заявка или вариант не найдены."
		case errors.Is(err, app.ErrConflict):
			code, message = 409, "Заявка или запас изменились. Обновите страницу."
		case errors.Is(err, app.ErrInvalid), errors.Is(err, domain.ErrInvalidRequest), errors.Is(err, domain.ErrInvalidRule):
			code, message = 422, err.Error()
		}
		httpx.WriteProblem(w, r, code, "Операция подбора не выполнена", message)
	}})
	gen.HandlerWithOptions(strict, gen.StdHTTPServerOptions{BaseRouter: mux, Middlewares: []gen.MiddlewareFunc{func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Pragma", "no-cache")
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
			op, ok := Operations[r.Pattern]
			if !ok {
				httpx.WriteProblem(w, r, 403, "Нет доступа", "Операция не описана.")
				return
			}
			auth.Require(op, onDenied)(next).ServeHTTP(w, r)
		})
	}}})
}

func (h *Handler) CreateRequest(ctx context.Context, req gen.CreateRequestRequestObject) (gen.CreateRequestResponseObject, error) {
	if req.Body == nil || req.Body.IdempotencyKey == uuid.Nil {
		return nil, app.ErrInvalid
	}
	a := Actor(ctx)
	b := req.Body
	r := domain.Request{RequestID: b.IdempotencyKey, ClinicID: a.ClinicID, ComponentKind: b.ComponentKind, RequestedUnits: b.RequestedUnits, Urgency: b.Urgency, BloodGroup: b.BloodGroup, Phenotype: b.Phenotype}
	for _, c := range b.Constraints {
		r.Constraints = append(r.Constraints, domain.Constraint{Code: c.Code, Value: c.Value})
	}
	n, err := h.svc.Create(ctx, r, b.PatientCaseNo, a)
	if err != nil {
		return nil, err
	}
	return gen.CreateRequest201JSONResponse(requestView(n)), nil
}
func (h *Handler) ListRequests(ctx context.Context, _ gen.ListRequestsRequestObject) (gen.ListRequestsResponseObject, error) {
	items, err := h.svc.Requests(ctx, Actor(ctx))
	if err != nil {
		return nil, err
	}
	out := make(gen.ListRequests200JSONResponse, 0, len(items))
	for _, n := range items {
		out = append(out, requestView(n))
	}
	return out, nil
}
func (h *Handler) GetRequest(ctx context.Context, req gen.GetRequestRequestObject) (gen.GetRequestResponseObject, error) {
	n, err := h.svc.Request(ctx, req.Id, Actor(ctx))
	if err != nil {
		return nil, err
	}
	return gen.GetRequest200JSONResponse(requestView(n)), nil
}
func (h *Handler) GetVariants(ctx context.Context, req gen.GetVariantsRequestObject) (gen.GetVariantsResponseObject, error) {
	n, err := h.svc.Request(ctx, req.Id, Actor(ctx))
	if err != nil {
		return nil, err
	}
	return gen.GetVariants200JSONResponse(requestView(n).Variants), nil
}
func (h *Handler) SelectVariant(ctx context.Context, req gen.SelectVariantRequestObject) (gen.SelectVariantResponseObject, error) {
	if req.Body == nil {
		return nil, app.ErrInvalid
	}
	n, err := h.svc.Select(ctx, req.Id, req.Body.VariantOfferId, req.Body.ChoiceId, Actor(ctx))
	if err != nil {
		return nil, err
	}
	return gen.SelectVariant200JSONResponse(requestView(n)), nil
}
func (h *Handler) CancelRequest(ctx context.Context, req gen.CancelRequestRequestObject) (gen.CancelRequestResponseObject, error) {
	if err := h.svc.Cancel(ctx, req.Id, Actor(ctx)); err != nil {
		return nil, err
	}
	return gen.CancelRequest204Response{}, nil
}
func (h *Handler) ReadPatientCase(ctx context.Context, req gen.ReadPatientCaseRequestObject) (gen.ReadPatientCaseResponseObject, error) {
	plain, err := h.svc.PatientCase(ctx, req.Id, Actor(ctx))
	if err != nil {
		return nil, err
	}
	return gen.ReadPatientCase200JSONResponse{PatientCaseNo: plain}, nil
}

func requestView(n app.RequestRecord) gen.Request {
	r := n.Request
	out := gen.Request{ClinicalRequestId: r.RequestID, ClinicId: r.ClinicID, CreatedBy: n.CreatedBy, Status: n.Status, ComponentKind: r.ComponentKind, RequestedUnits: r.RequestedUnits, Urgency: r.Urgency, BloodGroup: r.BloodGroup, CreatedAt: r.CreatedAt, StockWatermark: n.Result.StockWatermark, StockSyncedAt: n.Result.StockSyncedAt, Variants: []gen.Variant{}, Notes: append([]string{}, n.Result.Notes...)}
	out.PatientAllocationStatus = n.PatientAllocationStatus
	out.ClinicalUseBlocked = n.ClinicalUseBlocked
	out.Obligations = make([]gen.RequestObligation, 0, len(n.Obligations))
	for _, o := range n.Obligations {
		out.Obligations = append(out.Obligations, gen.RequestObligation{ObligationId: o.ID, SourceClinicId: o.Source, Status: o.Status, Units: o.Units, ClinicalUseBlocked: o.ClinicalUseBlocked})
	}
	if out.PatientAllocationStatus == "" {
		out.PatientAllocationStatus = "NOT_REQUESTED"
	}
	if n.Selection != nil {
		out.SelectedVariantId = &n.Selection.VariantID
	}
	for _, v := range n.Result.Variants {
		out.Variants = append(out.Variants, gen.Variant{VariantOfferId: app.VariantID(r.RequestID, v.Rank), RankNo: v.Rank, SourceClinicId: v.SourceClinicID, IsLocal: v.IsLocal, ComponentKind: v.ComponentKind, BloodGroup: v.BloodGroup, AvailableUnits: v.AvailableUnits, OfferedUnits: len(v.UnitIDs), DeliveryMinutes: v.Delivery.Minutes, DeliveryStatus: string(v.Delivery.Status), IsDeficitStockUsed: v.IsDeficitStockUsed, ProtectedKnown: v.ProtectedKnown, Explanation: v.Explanation, RuleCode: v.Rule.Code, RuleVersion: v.Rule.Version, UnitIds: append([]uuid.UUID{}, v.UnitIDs...)})
	}
	return out
}
func (h *Handler) ListRules(ctx context.Context, _ gen.ListRulesRequestObject) (gen.ListRulesResponseObject, error) {
	items, err := h.svc.Rules(ctx)
	if err != nil {
		return nil, err
	}
	out := make(gen.ListRules200JSONResponse, 0, len(items))
	for _, r := range items {
		v := gen.Rule{MatchingRuleId: r.RuleID, RuleCode: r.Code, VersionNo: r.Version, ComponentKind: r.ComponentKind, RequestedGroup: r.RequestedGroup, AllowedGroup: r.AllowedGroup, AllowedKind: r.AllowedKind, ApplicationCondition: r.Condition, IsException: r.IsException, Priority: r.Priority, BasisDocument: r.BasisDocument, BasisClause: r.BasisClause, ValidFrom: r.ValidFrom}
		if !r.ValidTo.IsZero() {
			end := r.ValidTo
			v.ValidTo = &end
		}
		out = append(out, v)
	}
	return out, nil
}
func (h *Handler) AddRuleVersion(ctx context.Context, req gen.AddRuleVersionRequestObject) (gen.AddRuleVersionResponseObject, error) {
	if req.Body == nil || req.Body.RuleCode != req.Code {
		return nil, app.ErrInvalid
	}
	b := req.Body
	until := time.Time{}
	if b.ValidTo != nil {
		until = *b.ValidTo
	}
	r := domain.Rule{RuleID: b.MatchingRuleId, Code: req.Code, Version: b.VersionNo, ComponentKind: b.ComponentKind, RequestedGroup: b.RequestedGroup, AllowedGroup: b.AllowedGroup, AllowedKind: b.AllowedKind, Condition: b.ApplicationCondition, IsException: b.IsException, Priority: b.Priority, BasisDocument: b.BasisDocument, BasisClause: b.BasisClause, ValidFrom: b.ValidFrom, ValidTo: until}
	if err := h.svc.AddRule(ctx, r, Actor(ctx)); err != nil {
		return nil, err
	}
	return gen.AddRuleVersion204Response{}, nil
}
