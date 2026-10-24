package api

import (
	"context"
	"errors"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/demand/internal/api/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/demand/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/demand/internal/domain"
)

func estimateView(n app.EstimateRecord) gen.Estimate {
	out := gen.Estimate{DemandEstimateId: n.ID, VersionNo: n.Version, Status: n.Status, ProfileFileId: n.Selection.ProfileFileID, ClinicShareVersionId: n.Selection.ShareVersionID, HistoryIds: n.Selection.HistoryIDs, PeriodStart: openapi_types.Date{Time: n.Selection.Period.Start}, PeriodEnd: openapi_types.Date{Time: n.Selection.Period.End}, Notice: domain.DemoNotice, Lines: make([]gen.EstimateLine, 0, len(n.Lines))}
	if n.ConfirmedBy != "" {
		out.ConfirmedBy = &n.ConfirmedBy
	}
	if n.RejectionReason != "" {
		out.RejectionReason = &n.RejectionReason
	}
	for _, line := range n.Lines {
		out.Lines = append(out.Lines, gen.EstimateLine{ClinicId: line.ClinicID, ComponentKind: line.ComponentKind, BloodGroup: line.BloodGroup, DemandVolumeMl: line.DemandVolumeML})
	}
	return out
}

func (h *Handler) CalculateEstimate(ctx context.Context, req gen.CalculateEstimateRequestObject) (gen.CalculateEstimateResponseObject, error) {
	if req.Body == nil {
		return gen.CalculateEstimate422ApplicationProblemPlusJSONResponse{Title: "Не выбраны входы", Status: 422}, nil
	}
	p, _ := auth.FromContext(ctx)
	n, err := h.svc.Calculate(ctx, app.EstimateSelection{ProfileFileID: req.Body.ProfileFileId, ShareVersionID: req.Body.ClinicShareVersionId, HistoryIDs: req.Body.HistoryIds, Period: domain.Period{Start: req.Body.PeriodStart.Time, End: req.Body.PeriodEnd.Time}}, app.Actor{UserID: userID(p), Role: p.PrimaryRole()})
	if errors.Is(err, domain.ErrEstimateInput) {
		detail := err.Error()
		return gen.CalculateEstimate422ApplicationProblemPlusJSONResponse{Title: "Входы оценки не согласованы", Status: 422, Detail: &detail}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CalculateEstimate200JSONResponse(estimateView(n)), nil
}

func (h *Handler) ListEstimates(ctx context.Context, _ gen.ListEstimatesRequestObject) (gen.ListEstimatesResponseObject, error) {
	items, err := h.svc.Estimates(ctx)
	if err != nil {
		return nil, err
	}
	out := make(gen.ListEstimates200JSONResponse, 0, len(items))
	for _, n := range items {
		out = append(out, estimateView(n))
	}
	return out, nil
}

func (h *Handler) GetEstimate(ctx context.Context, req gen.GetEstimateRequestObject) (gen.GetEstimateResponseObject, error) {
	n, err := h.svc.Estimate(ctx, req.Id)
	if errors.Is(err, app.ErrNotFound) {
		return gen.GetEstimate404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: fileNotFound()}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetEstimate200JSONResponse(estimateView(n)), nil
}

func (h *Handler) PublishEstimate(ctx context.Context, req gen.PublishEstimateRequestObject) (gen.PublishEstimateResponseObject, error) {
	p, _ := auth.FromContext(ctx)
	n, err := h.svc.PublishEstimate(ctx, req.Id, app.Actor{UserID: userID(p), Role: p.PrimaryRole()})
	if errors.Is(err, app.ErrNotFound) {
		return gen.PublishEstimate404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: fileNotFound()}, nil
	}
	if errors.Is(err, domain.ErrEstimateInput) {
		detail := err.Error()
		return gen.PublishEstimate422ApplicationProblemPlusJSONResponse{Title: "Версия не может быть опубликована", Status: 422, Detail: &detail}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.PublishEstimate200JSONResponse(estimateView(n)), nil
}

func (h *Handler) RejectEstimate(ctx context.Context, req gen.RejectEstimateRequestObject) (gen.RejectEstimateResponseObject, error) {
	if req.Body == nil {
		return gen.RejectEstimate422ApplicationProblemPlusJSONResponse{Title: "Укажите причину", Status: 422}, nil
	}
	p, _ := auth.FromContext(ctx)
	n, err := h.svc.RejectEstimate(ctx, req.Id, req.Body.Reason, app.Actor{UserID: userID(p), Role: p.PrimaryRole()})
	if errors.Is(err, app.ErrNotFound) {
		return gen.RejectEstimate404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: fileNotFound()}, nil
	}
	if errors.Is(err, domain.ErrEstimateInput) {
		detail := err.Error()
		return gen.RejectEstimate422ApplicationProblemPlusJSONResponse{Title: "Версия не может быть отклонена", Status: 422, Detail: &detail}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.RejectEstimate200JSONResponse(estimateView(n)), nil
}
