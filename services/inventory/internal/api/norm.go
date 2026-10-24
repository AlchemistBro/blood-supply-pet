package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/api/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/app"
)

func normView(n app.NormRecord) gen.StockNorm {
	return gen.StockNorm{StockNormId: n.ID, ClinicId: n.ClinicID, ComponentKind: n.Kind, ConsumptionYear: n.Year, AnnualVolumeMl: n.Annual, AvgMonthlyMl: n.MonthlyML, NormMl: n.NormML, IsMinUnitApplied: n.Minimum, Version: n.Version, CalculatedAt: n.At}
}

func (h *Handler) SetAnnualConsumption(ctx context.Context, req gen.SetAnnualConsumptionRequestObject) (gen.SetAnnualConsumptionResponseObject, error) {
	if !h.clinicAllowed(ctx, req.Id) {
		return gen.SetAnnualConsumption403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbidden()}, nil
	}
	if req.Body == nil {
		return gen.SetAnnualConsumption400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: problem(http.StatusBadRequest, "Неверный расход", "Заполните год, вид и расход.")}, nil
	}
	p, _ := auth.FromContext(ctx)
	n, err := h.svc.SetAnnualConsumption(ctx, app.Actor{UserID: p.Username, Role: p.PrimaryRole(), ClinicID: p.ClinicID}, req.Id, req.Body.ComponentKind, req.Body.ConsumptionYear, req.Body.AnnualVolumeMl)
	if errors.Is(err, app.ErrInvalid) {
		return gen.SetAnnualConsumption400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: problem(http.StatusBadRequest, "Неверный расход", "Проверьте расход, год и вид компонента.")}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.SetAnnualConsumption200JSONResponse(normView(n)), nil
}

func (h *Handler) GetClinicNorms(ctx context.Context, req gen.GetClinicNormsRequestObject) (gen.GetClinicNormsResponseObject, error) {
	if !h.clinicAllowed(ctx, req.Id) {
		return gen.GetClinicNorms403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbidden()}, nil
	}
	items, err := h.svc.Norms(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	out := make(gen.GetClinicNorms200JSONResponse, 0, len(items))
	for _, n := range items {
		out = append(out, normView(n))
	}
	return out, nil
}

func (h *Handler) SyncClinic(ctx context.Context, req gen.SyncClinicRequestObject) (gen.SyncClinicResponseObject, error) {
	if !h.clinicAllowed(ctx, req.Id) {
		return gen.SyncClinic403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbidden()}, nil
	}
	p, _ := auth.FromContext(ctx)
	r, err := h.svc.SyncOnDemand(ctx, req.Id, app.Actor{UserID: p.Username, Role: p.PrimaryRole(), ClinicID: p.ClinicID})
	if err != nil {
		return nil, err
	}
	return gen.SyncClinic200JSONResponse{SyncRunId: r.RunID, Result: r.Result, Applied: r.Applied, Skipped: r.Skipped}, nil
}
