// Package api — HTTP-обработчики сервиса уведомлений по контракту api/openapi.yaml (строгий сервер oapi-codegen).
package api

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/api/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/domain"
)

// Lister — чтение журнала писем (реализует app.Service).
type Lister interface {
	List(ctx context.Context, f app.ListFilter) ([]domain.Notification, error)
}

// Handler реализует gen.StrictServerInterface.
type Handler struct {
	svc Lister
}

// Mount регистрирует маршруты на mux с проверкой прав по операции.
func Mount(mux *http.ServeMux, svc Lister, onDenied auth.Denied) {
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
		Middlewares: []gen.MiddlewareFunc{authorize(onDenied)},
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			httpx.WriteProblem(w, r, http.StatusBadRequest, "Неверный запрос", err.Error())
		},
	})
}

// authorize выбирает операцию матрицы прав по шаблону маршрута. Журнал писем — операция READ_AUDIT
// (DUTY_OFFICER, IT_SERVICE): отдельной операции для состояния интеграций в матрице пока нет.
func authorize(onDenied auth.Denied) gen.MiddlewareFunc {
	ops := map[string]auth.Operation{
		"GET /notifications": auth.OpReadAudit,
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			op, ok := ops[r.Pattern]
			if !ok {
				httpx.WriteProblem(w, r, http.StatusForbidden, "Недостаточно прав", "Маршрут не описан в матрице прав.")
				return
			}
			auth.Require(op, onDenied)(next).ServeHTTP(w, r)
		})
	}
}

// ListNotifications — GET /notifications.
func (h *Handler) ListNotifications(ctx context.Context, req gen.ListNotificationsRequestObject) (gen.ListNotificationsResponseObject, error) {
	var f app.ListFilter
	if req.Params.Status != nil {
		if !req.Params.Status.Valid() {
			detail := "Статус должен быть PENDING, SENT или FAILED."
			return gen.ListNotifications400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse{
				Title: "Неверный запрос", Status: http.StatusBadRequest, Detail: &detail,
			}}, nil
		}
		st := domain.Status(*req.Params.Status)
		f.Status = &st
	}
	if req.Params.Limit != nil {
		f.Limit = *req.Params.Limit
	}
	items, err := h.svc.List(ctx, f)
	if err != nil {
		return nil, err
	}
	out := gen.NotificationPage{Items: make([]gen.Notification, 0, len(items))}
	for _, n := range items {
		out.Items = append(out.Items, toAPI(n))
	}
	return gen.ListNotifications200JSONResponse(out), nil
}

func toAPI(n domain.Notification) gen.Notification {
	out := gen.Notification{
		NotificationId: openapi_types.UUID(n.ID), Kind: gen.NotificationKind(n.Kind),
		ObligationId:  openapi_types.UUID(n.Facts.ObligationID),
		RecipientRole: gen.NotificationRecipientRole(n.To.Role), RecipientClinicId: n.To.ClinicID,
		Subject: n.Subject, Status: gen.NotificationStatus(n.Status), Attempts: n.Attempts,
		NextAttemptAt: n.NextAttemptAt, CreatedAt: n.CreatedAt, SentAt: n.SentAt,
		RecipientEmail: nullable(n.Email), LastError: nullable(n.LastError),
	}
	if n.Facts.RequestID != uuid.Nil {
		id := openapi_types.UUID(n.Facts.RequestID)
		out.RequestId = &id
	}
	return out
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
