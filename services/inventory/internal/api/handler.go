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

// operations — операция матрицы прав для каждого маршрута; совпадает с x-bs-operation в openapi.yaml
// (проверяет TestEveryOperationHasPolicy).
var operations = map[string]auth.Operation{
	"POST /dev/seed-norms":                 auth.OpSeedDemo,
	"GET /internal/sync-status":            auth.OpReadAudit,
	"PUT /clinics/{id}/annual-consumption": auth.OpEnterConsumption,
	"GET /clinics/{id}/norms":              auth.OpViewStock,
	"POST /clinics/{id}/sync":              auth.OpSyncOnDemand,
	routeUnitStatus:                        auth.OpInternalUnitStatus,
	routeProtectedVolume:                   auth.OpDevPanel,
	"GET /clinics/{id}/stock":              auth.OpViewStock,
	"GET /clinics/{id}/units":              auth.OpViewStock,
	"GET /units/{id}":                      auth.OpViewStock,
	"GET /units/{id}/history":              auth.OpViewStock,
	"GET /sync/last":                       auth.OpViewStock,
	"GET /sync/runs":                       auth.OpViewStock,
}

// Options — что включено.
type Options struct {
	SeedEnabled bool
	// DevEnabled — демо-маршруты /dev/* (только BS_ENV=local); иначе они отвечают 404.
	DevEnabled bool
}

// Handler реализует gen.StrictServerInterface.
type Handler struct {
	svc      *app.Service
	onDenied auth.Denied
}

// Mount регистрирует маршруты на mux с проверкой прав по операции.
func Mount(mux *http.ServeMux, svc *app.Service, onDenied auth.Denied, o Options) {
	h := &Handler{svc: svc, onDenied: onDenied}
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
// Запрос и операция кладутся в контекст: проверка клиники в обработчике пишет отказ в аудит тем же
// auth.Denied, что и матрица прав, а ему нужен *http.Request.
func authorize(onDenied auth.Denied, o Options) gen.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Pattern == routeProtectedVolume && !o.DevEnabled {
				httpx.WriteProblem(w, r, http.StatusNotFound, "Не найдено", "Демо-маршрут доступен только на локальном стенде.")
				return
			}
			if r.Pattern == "POST /dev/seed-norms" && !o.SeedEnabled {
				http.NotFound(w, r)
				return
			}
			op, ok := operations[r.Pattern]
			if !ok {
				httpx.WriteProblem(w, r, http.StatusForbidden, "Недостаточно прав", "Маршрут не описан в матрице прав.")
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), requestKey{}, requestInfo{r: r, op: op}))
			auth.Require(op, onDenied)(next).ServeHTTP(w, r)
		})
	}
}

type requestKey struct{}

type requestInfo struct {
	r  *http.Request
	op auth.Operation
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
