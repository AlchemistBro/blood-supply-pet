// Package api — HTTP-обработчики inventory по контракту api/openapi.yaml (строгий сервер oapi-codegen).
package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/api/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
)

const (
	routeUnitStatus      = "GET /internal/units/status"
	routeProtectedVolume = "POST /dev/protected-volume"
)

// Options — что включено.
type Options struct {
	// DevEnabled — демо-маршруты /dev/* (только BS_ENV=local); иначе они отвечают 404.
	DevEnabled bool
}

// Handler реализует gen.StrictServerInterface.
type Handler struct {
	svc *app.Service
}

// Mount регистрирует маршруты на mux с проверкой прав по операции.
func Mount(mux *http.ServeMux, svc *app.Service, onDenied auth.Denied, o Options) {
	h := &Handler{svc: svc}
	strict := gen.NewStrictHandlerWithOptions(h, nil, gen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			httpx.WriteProblem(w, r, http.StatusBadRequest, "Неверный запрос", err.Error())
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			httpx.WriteProblem(w, r, http.StatusInternalServerError, "Внутренняя ошибка", "Запрос не выполнен. Повторите позже.")
		},
	})
	gen.HandlerWithOptions(strict, gen.StdHTTPServerOptions{
		BaseRouter:  mux,
		Middlewares: []gen.MiddlewareFunc{authorize(onDenied, o)},
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			httpx.WriteProblem(w, r, http.StatusBadRequest, "Неверный запрос", err.Error())
		},
	})
}

// authorize выбирает операцию матрицы прав по шаблону маршрута. Демо-маршрут вне BS_ENV=local
// отвечает 404 до проверки прав: в рабочем окружении его как будто нет.
func authorize(onDenied auth.Denied, o Options) gen.MiddlewareFunc {
	ops := map[string]auth.Operation{
		routeUnitStatus:      auth.OpInternalUnitStatus,
		routeProtectedVolume: auth.OpDevPanel,
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Pattern == routeProtectedVolume && !o.DevEnabled {
				httpx.WriteProblem(w, r, http.StatusNotFound, "Не найдено", "Демо-маршрут доступен только на локальном стенде.")
				return
			}
			op, ok := ops[r.Pattern]
			if !ok {
				httpx.WriteProblem(w, r, http.StatusForbidden, "Недостаточно прав", "Маршрут не описан в матрице прав.")
				return
			}
			auth.Require(op, onDenied)(next).ServeHTTP(w, r)
		})
	}
}

// GetUnitStatuses — GET /internal/units/status (ADR-0004).
func (h *Handler) GetUnitStatuses(ctx context.Context, req gen.GetUnitStatusesRequestObject) (gen.GetUnitStatusesResponseObject, error) {
	ids := make([]uuid.UUID, 0, len(req.Params.Ids))
	for _, id := range req.Params.Ids {
		ids = append(ids, uuid.UUID(id))
	}
	rep, err := h.svc.UnitStatuses(ctx, ids)
	if err != nil {
		if errors.Is(err, app.ErrInvalid) {
			return gen.GetUnitStatuses400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: problem(http.StatusBadRequest, "Неверный список единиц", err.Error())}, nil
		}
		return nil, err
	}
	out := gen.UnitStatusReport{
		Source: gen.UnitStatusReportSource(rep.Source), CheckedAt: rep.CheckedAt, Watermark: rep.Watermark,
		Units: make([]gen.UnitStatus, 0, len(rep.Units)), NotFound: make([]openapi_types.UUID, 0, len(rep.NotFound)),
	}
	for _, u := range rep.Units {
		out.Units = append(out.Units, gen.UnitStatus{
			UnitId: openapi_types.UUID(u.UnitID), ClinicId: u.ClinicID, ComponentKind: u.ComponentKind,
			Status: gen.UnitStatusStatus(u.Status), Available: u.Available, Version: u.Version,
			ChangedAt: u.ChangedAt, ExpiresAt: u.ExpiresAt,
		})
	}
	for _, id := range rep.NotFound {
		out.NotFound = append(out.NotFound, openapi_types.UUID(id))
	}
	return gen.GetUnitStatuses200JSONResponse(out), nil
}

// PublishProtectedVolume — POST /dev/protected-volume (демо до появления planning).
func (h *Handler) PublishProtectedVolume(ctx context.Context, req gen.PublishProtectedVolumeRequestObject) (gen.PublishProtectedVolumeResponseObject, error) {
	if req.Body == nil {
		return gen.PublishProtectedVolume400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: problem(http.StatusBadRequest, "Неверный запрос", "Пустое тело")}, nil
	}
	in := domain.ProtectedVolume{ClinicID: req.Body.ClinicId, ComponentKind: req.Body.ComponentKind, VolumeUnits: req.Body.VolumeUnits}
	if req.Body.Reason != nil {
		in.Reason = *req.Body.Reason
	}
	p, _ := auth.FromContext(ctx)
	pv, err := h.svc.PublishProtectedVolume(ctx, app.Actor{UserID: p.Username, Role: p.PrimaryRole(), ClinicID: p.ClinicID}, in)
	if err != nil {
		if errors.Is(err, app.ErrInvalid) {
			return gen.PublishProtectedVolume400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: problem(http.StatusBadRequest, "Неверный защищённый объём", err.Error())}, nil
		}
		return nil, err
	}
	return gen.PublishProtectedVolume202JSONResponse{
		ClinicId: pv.ClinicID, ComponentKind: pv.ComponentKind, VolumeUnits: pv.VolumeUnits, Version: pv.Version, Reason: pv.Reason,
	}, nil
}

func problem(status int, title, detail string) gen.BadRequestApplicationProblemPlusJSONResponse {
	return gen.BadRequestApplicationProblemPlusJSONResponse{Title: title, Status: status, Detail: &detail}
}
