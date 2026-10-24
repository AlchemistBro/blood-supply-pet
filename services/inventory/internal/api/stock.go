package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/api/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
)

// Просмотр запаса (VIEW_STOCK): оператор и врач видят только свою клинику, дежурный и ИТ-служба — любую
// (auth.SameClinic). Исходный код единицы АИСТ-М не отдаётся никому, в том числе ИТ-службе (ADR-0010).

// clinicAllowed проверяет клинику участника; при отказе пишет ACCESS_DENIED в аудит.
func (h *Handler) clinicAllowed(ctx context.Context, clinicID int64) bool {
	p, ok := auth.FromContext(ctx)
	if ok && auth.SameClinic(p, clinicID) {
		return true
	}
	if info, ok := ctx.Value(requestKey{}).(requestInfo); ok && h.onDenied != nil {
		h.onDenied(info.r, &p, info.op)
	}
	return false
}

func forbidden() gen.ForbiddenApplicationProblemPlusJSONResponse {
	d := "Запас другой клиники недоступен вашей роли."
	return gen.ForbiddenApplicationProblemPlusJSONResponse{Title: "Недостаточно прав", Status: http.StatusForbidden, Detail: &d}
}

func notFound(detail string) gen.NotFoundApplicationProblemPlusJSONResponse {
	return gen.NotFoundApplicationProblemPlusJSONResponse{Title: "Не найдено", Status: http.StatusNotFound, Detail: &detail}
}

// GetClinicStock — GET /clinics/{id}/stock.
func (h *Handler) GetClinicStock(ctx context.Context, req gen.GetClinicStockRequestObject) (gen.GetClinicStockResponseObject, error) {
	if !h.svc.ClinicExists(req.Id) {
		return gen.GetClinicStock404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFound("Клиники нет в справочнике.")}, nil
	}
	if !h.clinicAllowed(ctx, req.Id) {
		return gen.GetClinicStock403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbidden()}, nil
	}
	s, err := h.svc.Stock(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	out := gen.ClinicStock{ClinicId: s.ClinicID, AsOf: s.AsOf, Watermark: s.Watermark, IsStale: s.Stale,
		Lines: make([]gen.StockLine, 0, len(s.Lines))}
	for _, l := range s.Lines {
		out.Lines = append(out.Lines, gen.StockLine{
			ComponentKind: l.ComponentKind, BloodGroup: l.BloodGroup, UnitCount: l.UnitCount,
			VolumeMl: l.VolumeML, VolumeL: domain.Litres(l.VolumeML), NearestExpiry: l.NearestExpiry,
		})
	}
	return gen.GetClinicStock200JSONResponse(out), nil
}

// ListClinicUnits — GET /clinics/{id}/units (FEFO по умолчанию).
func (h *Handler) ListClinicUnits(ctx context.Context, req gen.ListClinicUnitsRequestObject) (gen.ListClinicUnitsResponseObject, error) {
	if !h.svc.ClinicExists(req.Id) {
		return gen.ListClinicUnits404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFound("Клиники нет в справочнике.")}, nil
	}
	if !h.clinicAllowed(ctx, req.Id) {
		return gen.ListClinicUnits403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbidden()}, nil
	}
	f := app.UnitFilter{ClinicID: req.Id, FEFO: true}
	p := req.Params
	if p.Fefo != nil {
		f.FEFO = *p.Fefo
	}
	if p.Kind != nil {
		f.ComponentKind = *p.Kind
	}
	if p.Group != nil {
		f.BloodGroup = *p.Group
	}
	if p.Limit != nil {
		f.Limit = *p.Limit
		if f.Limit == 0 {
			f.Limit = -1 // явный ноль — ошибка, а не значение по умолчанию
		}
	}
	if p.Offset != nil {
		f.Offset = *p.Offset
	}
	page, err := h.svc.Units(ctx, f)
	if err != nil {
		if errors.Is(err, app.ErrInvalid) {
			return gen.ListClinicUnits400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: problem(http.StatusBadRequest, "Неверный фильтр", err.Error())}, nil
		}
		return nil, err
	}
	out := gen.UnitPage{ClinicId: page.ClinicID, AsOf: page.AsOf, Watermark: page.Watermark, IsStale: page.Stale,
		HasMore: page.HasMore, Units: make([]gen.UnitCard, 0, len(page.Units))}
	for _, u := range page.Units {
		out.Units = append(out.Units, card(u))
	}
	return gen.ListClinicUnits200JSONResponse(out), nil
}

// card переводит единицу в контракт. aist_unit_code сюда намеренно не попадает (ADR-0010).
func card(u app.UnitView) gen.UnitCard {
	c := gen.UnitCard{
		UnitId: openapi_types.UUID(u.UnitID), ClinicId: u.ClinicID, StorageLocation: u.StorageLocation,
		ComponentKind: u.ComponentKind, BloodGroup: u.BloodGroup, RhPositive: u.RhPositive, VolumeMl: u.VolumeML,
		ExpiresAt: u.ExpiresAt, Status: gen.UnitCardStatus(u.Status), Available: u.Available, IsExpired: u.Expired,
		Version: u.Version, StatusReceivedAt: u.StatusReceivedAt,
	}
	if u.Phenotype != "" {
		ph := u.Phenotype
		c.Phenotype = &ph
	}
	return c
}

// GetUnit — GET /units/{id}.
func (h *Handler) GetUnit(ctx context.Context, req gen.GetUnitRequestObject) (gen.GetUnitResponseObject, error) {
	u, err := h.svc.Unit(ctx, uuid.UUID(req.Id))
	if errors.Is(err, app.ErrNotFound) {
		return gen.GetUnit404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFound("Единицы нет в копии запаса.")}, nil
	}
	if err != nil {
		return nil, err
	}
	if !h.clinicAllowed(ctx, u.ClinicID) {
		return gen.GetUnit403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbidden()}, nil
	}
	return gen.GetUnit200JSONResponse(card(u)), nil
}

// GetUnitHistory — GET /units/{id}/history.
func (h *Handler) GetUnitHistory(ctx context.Context, req gen.GetUnitHistoryRequestObject) (gen.GetUnitHistoryResponseObject, error) {
	u, items, err := h.svc.UnitHistory(ctx, uuid.UUID(req.Id))
	if errors.Is(err, app.ErrNotFound) {
		return gen.GetUnitHistory404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFound("Единицы нет в копии запаса.")}, nil
	}
	if err != nil {
		return nil, err
	}
	if !h.clinicAllowed(ctx, u.ClinicID) {
		return gen.GetUnitHistory403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbidden()}, nil
	}
	out := gen.UnitHistory{UnitId: req.Id, Items: make([]gen.UnitStatusChange, 0, len(items))}
	for _, it := range items {
		out.Items = append(out.Items, gen.UnitStatusChange{
			Status: gen.UnitStatusChangeStatus(it.Status), SourceVersion: it.SourceVersion,
			ChangedAt: it.ChangedAt, ReceivedAt: it.ReceivedAt,
		})
	}
	return gen.GetUnitHistory200JSONResponse(out), nil
}

// GetLastSync — GET /sync/last.
func (h *Handler) GetLastSync(ctx context.Context, _ gen.GetLastSyncRequestObject) (gen.GetLastSyncResponseObject, error) {
	o, err := h.svc.LastSync(ctx)
	if err != nil {
		return nil, err
	}
	out := gen.SyncOverview{AsOf: o.AsOf, Watermark: o.Watermark, IsStale: o.Stale}
	if o.LastRun != nil {
		r := syncRun(*o.LastRun)
		out.LastRun = &r
	}
	if o.LastSuccess != nil {
		r := syncRun(*o.LastSuccess)
		out.LastSuccess = &r
	}
	return gen.GetLastSync200JSONResponse(out), nil
}

// ListSyncRuns — GET /sync/runs.
func (h *Handler) ListSyncRuns(ctx context.Context, req gen.ListSyncRunsRequestObject) (gen.ListSyncRunsResponseObject, error) {
	f := app.SyncRunFilter{}
	p := req.Params
	if p.Kind != nil {
		f.Kind = string(*p.Kind)
	}
	if p.Before != nil {
		b := uuid.UUID(*p.Before)
		f.BeforeID = &b
	}
	if p.Limit != nil {
		f.Limit = *p.Limit
		if f.Limit == 0 {
			f.Limit = -1
		}
	}
	page, err := h.svc.SyncRuns(ctx, f)
	if err != nil {
		if errors.Is(err, app.ErrInvalid) {
			return gen.ListSyncRuns400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: problem(http.StatusBadRequest, "Неверный фильтр", err.Error())}, nil
		}
		return nil, err
	}
	out := gen.SyncRunPage{Items: make([]gen.SyncRun, 0, len(page.Runs))}
	for _, r := range page.Runs {
		out.Items = append(out.Items, syncRun(r))
	}
	if page.NextBefore != nil {
		next := openapi_types.UUID(*page.NextBefore)
		out.NextBefore = &next
	}
	return gen.ListSyncRuns200JSONResponse(out), nil
}

func syncRun(r app.SyncRunView) gen.SyncRun {
	out := gen.SyncRun{
		SyncRunId: openapi_types.UUID(r.ID), SyncKind: gen.SyncRunSyncKind(r.Kind), RequestedBy: r.RequestedBy,
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, ChangesCount: r.ChangesCount, ErrorText: r.Note,
		SourceVersion: r.SourceVersion,
	}
	if r.Result != nil {
		res := gen.SyncRunSyncResult(*r.Result)
		out.SyncResult = &res
	}
	return out
}
