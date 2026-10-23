// Package api — HTTP-обработчики transfer по контракту api/openapi.yaml (строгий сервер oapi-codegen).
// Права проверяются дважды: роль по матрице (операция из x-bs-operation маршрута) и клиника — оператор
// подтверждает и заменяет только задания своей клиники-источника (auth.SameClinic), иначе 403 и аудит.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/api/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

// Operations — операция матрицы прав для каждого маршрута. Совпадает с x-bs-operation в openapi.yaml
// (это проверяет тест контракта).
var Operations = map[string]auth.Operation{
	"POST /obligations":              auth.OpOpenObligation,
	"GET /obligations":               auth.OpViewObligation,
	"GET /obligations/{id}":          auth.OpViewObligation,
	"POST /obligations/{id}/confirm": auth.OpConfirmTransfer,
	"POST /obligations/{id}/replace": auth.OpReplaceUnit,
	"POST /obligations/{id}/cancel":  auth.OpCancelObligation,
}

// Handler реализует gen.StrictServerInterface.
type Handler struct {
	svc *app.Service
	ref *refdata.Set
	log *slog.Logger
}

// Mount регистрирует маршруты на mux с проверкой прав по операции.
func Mount(mux *http.ServeMux, svc *app.Service, ref *refdata.Set, log *slog.Logger, onDenied auth.Denied) {
	h := &Handler{svc: svc, ref: ref, log: log}
	strict := gen.NewStrictHandlerWithOptions(h, nil, gen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			httpx.WriteProblem(w, r, http.StatusBadRequest, "Неверный запрос", err.Error())
		},
		ResponseErrorHandlerFunc: errorHandler(log, onDenied),
	})
	gen.HandlerWithOptions(strict, gen.StdHTTPServerOptions{
		BaseRouter:  mux,
		Middlewares: []gen.MiddlewareFunc{authorize(onDenied)},
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			httpx.WriteProblem(w, r, http.StatusBadRequest, "Неверный запрос", err.Error())
		},
	})
}

// authorize выбирает операцию матрицы прав по шаблону маршрута (r.Pattern заполняет ServeMux).
func authorize(onDenied auth.Denied) gen.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			op, ok := Operations[r.Pattern]
			if !ok {
				httpx.WriteProblem(w, r, http.StatusForbidden, "Недостаточно прав", "Маршрут не описан в матрице прав.")
				return
			}
			auth.Require(op, onDenied)(next).ServeHTTP(w, r)
		})
	}
}

// Ошибки обработчиков; errorHandler переводит их в problem+json.
var (
	errBadRequest = errors.New("неверный запрос")
)

// deniedError — роль подходит, но задание другой клиники: 403 и ACCESS_DENIED в аудит.
type deniedError struct{ op auth.Operation }

func (e deniedError) Error() string {
	return "задание другой клиники: " + string(e.op)
}

func errorHandler(log *slog.Logger, onDenied auth.Denied) func(w http.ResponseWriter, r *http.Request, err error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		var denied deniedError
		switch {
		case errors.As(err, &denied):
			p, ok := auth.FromContext(r.Context())
			if onDenied != nil && ok {
				onDenied(r, &p, denied.op)
			}
			httpx.WriteProblem(w, r, http.StatusForbidden, "Недостаточно прав",
				"Задание принадлежит другой клинике. Действовать можно только по заданиям своей клиники.")
		case errors.Is(err, app.ErrNotFound):
			httpx.WriteProblem(w, r, http.StatusNotFound, "Задание не найдено", "Проверьте номер задания или обновите список.")
		case errors.Is(err, errBadRequest), errors.Is(err, domain.ErrInvalidObligation), errors.Is(err, app.ErrInvalidEvent):
			httpx.WriteProblem(w, r, http.StatusBadRequest, "Неверный запрос", err.Error())
		default:
			log.ErrorContext(r.Context(), "ошибка обработки запроса", "path", r.URL.Path, "err", err)
			httpx.WriteProblem(w, r, http.StatusInternalServerError, "Внутренняя ошибка", "Запрос не выполнен. Повторите позже.")
		}
	}
}

func principal(ctx context.Context) (auth.Principal, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return auth.Principal{}, errors.New("нет участника запроса")
	}
	return p, nil
}

// visible — участник видит задание своей клиники (источник или получатель); дежурный и служба — все.
func visible(p auth.Principal, o domain.Obligation) bool {
	return auth.SameClinic(p, o.SourceClinicID) || auth.SameClinic(p, o.TargetClinicID)
}

// OpenObligation — POST /obligations (служебный токен matching или planning).
func (h *Handler) OpenObligation(ctx context.Context, req gen.OpenObligationRequestObject) (gen.OpenObligationResponseObject, error) {
	if req.Body == nil {
		return nil, fmt.Errorf("%w: нет тела запроса", errBadRequest)
	}
	n, err := h.newObligation(*req.Body)
	if err != nil {
		return nil, err
	}
	id, err := h.svc.Open(ctx, nil, n)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	return gen.OpenObligation200JSONResponse(toObligation(v)), nil
}

func (h *Handler) newObligation(b gen.OpenObligationInput) (domain.NewObligation, error) {
	switch {
	case !b.Origin.Valid():
		return domain.NewObligation{}, fmt.Errorf("%w: неизвестное происхождение %q", errBadRequest, b.Origin)
	case !b.Urgency.Valid():
		return domain.NewObligation{}, fmt.Errorf("%w: неизвестная срочность %q", errBadRequest, b.Urgency)
	case b.ConfirmWindowSeconds <= 0:
		return domain.NewObligation{}, fmt.Errorf("%w: confirm_window_seconds должно быть больше нуля", errBadRequest)
	}
	if _, ok := h.ref.Clinic(b.SourceClinicId); !ok {
		return domain.NewObligation{}, fmt.Errorf("%w: неизвестная клиника-источник %d", errBadRequest, b.SourceClinicId)
	}
	if _, ok := h.ref.Clinic(b.TargetClinicId); !ok {
		return domain.NewObligation{}, fmt.Errorf("%w: неизвестная клиника-получатель %d", errBadRequest, b.TargetClinicId)
	}
	if _, ok := h.ref.Kind(b.ComponentKind); !ok {
		return domain.NewObligation{}, fmt.Errorf("%w: неизвестный вид компонента %q", errBadRequest, b.ComponentKind)
	}
	var fallback []int64
	if b.FallbackSources != nil {
		fallback = *b.FallbackSources
	}
	return domain.NewObligation{
		ChoiceID: b.ChoiceId, RequestID: b.RequestId, Origin: domain.Origin(b.Origin),
		SourceClinicID: b.SourceClinicId, TargetClinicID: b.TargetClinicId, ComponentKind: b.ComponentKind,
		RequestedQty: b.RequestedQty, UrgencyCode: string(b.Urgency),
		ConfirmWindow: time.Duration(b.ConfirmWindowSeconds) * time.Second, FallbackSources: fallback,
		Spec: domain.ReplacementSpec{
			BloodGroup: b.Spec.BloodGroup, RhPositive: b.Spec.RhPositive, Phenotype: nonEmpty(b.Spec.Phenotype),
			MinExpiresAt: b.Spec.MinExpiresAt, RuleVersion: b.Spec.RuleVersion,
		},
	}, nil
}

// ListObligations — GET /obligations?requestId=|sourceClinicId=&status=.
func (h *Handler) ListObligations(ctx context.Context, req gen.ListObligationsRequestObject) (gen.ListObligationsResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	var status *domain.Status
	if req.Params.Status != nil {
		st, err := domain.ParseStatus(string(*req.Params.Status))
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errBadRequest, err)
		}
		status = &st
	}
	var list []app.ObligationView
	switch {
	case req.Params.RequestId != nil:
		all, err := h.svc.ByRequest(ctx, *req.Params.RequestId)
		if err != nil {
			return nil, err
		}
		for _, v := range all {
			if status != nil && v.Status != *status {
				continue
			}
			if req.Params.SourceClinicId != nil && v.SourceClinicID != *req.Params.SourceClinicId {
				continue
			}
			list = append(list, v)
		}
	case req.Params.SourceClinicId != nil:
		// Список чужого источника оператору не положен: явный отказ с аудитом, а не пустой ответ.
		if !auth.SameClinic(p, *req.Params.SourceClinicId) {
			return nil, deniedError{op: auth.OpViewObligation}
		}
		if list, err = h.svc.BySource(ctx, *req.Params.SourceClinicId, status); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w: нужен requestId или sourceClinicId", errBadRequest)
	}
	out := gen.ObligationList{Items: make([]gen.Obligation, 0, len(list))}
	for _, v := range list {
		if visible(p, v.Obligation) {
			out.Items = append(out.Items, toObligation(v))
		}
	}
	return gen.ListObligations200JSONResponse(out), nil
}

// GetObligation — GET /obligations/{id}.
func (h *Handler) GetObligation(ctx context.Context, req gen.GetObligationRequestObject) (gen.GetObligationResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.Find(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if !visible(p, v.Obligation) {
		return nil, deniedError{op: auth.OpViewObligation}
	}
	return gen.GetObligation200JSONResponse(toObligation(v)), nil
}

// sourceOperator проверяет, что участник действует от имени клиники-источника задания.
func (h *Handler) sourceOperator(ctx context.Context, id domain.Obligation, op auth.Operation) (auth.Principal, error) {
	p, err := principal(ctx)
	if err != nil {
		return auth.Principal{}, err
	}
	if !auth.SameClinic(p, id.SourceClinicID) {
		return auth.Principal{}, deniedError{op: op}
	}
	return p, nil
}

// ConfirmTransfer — POST /obligations/{id}/confirm. Ответ — один из восьми исходов в том же запросе.
func (h *Handler) ConfirmTransfer(ctx context.Context, req gen.ConfirmTransferRequestObject) (gen.ConfirmTransferResponseObject, error) {
	if req.Body == nil || len(req.Body.UnitIds) == 0 || req.Body.IdempotencyKey == "" {
		return nil, fmt.Errorf("%w: нужны unit_ids и idempotency_key", errBadRequest)
	}
	v, err := h.svc.Find(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	p, err := h.sourceOperator(ctx, v.Obligation, auth.OpConfirmTransfer)
	if err != nil {
		return nil, err
	}
	partial := req.Body.AcceptPartial != nil && *req.Body.AcceptPartial
	res, err := h.svc.Confirm(ctx, app.ConfirmTransfer{
		ObligationID: req.Id, UnitIDs: req.Body.UnitIds, AcceptPartial: partial,
		IdempotencyKey: req.Body.IdempotencyKey, Operator: p.Username,
	})
	if err != nil {
		return nil, err
	}
	return confirmationResponse(res)
}

// ReplaceUnit — POST /obligations/{id}/replace.
func (h *Handler) ReplaceUnit(ctx context.Context, req gen.ReplaceUnitRequestObject) (gen.ReplaceUnitResponseObject, error) {
	if req.Body == nil {
		return nil, fmt.Errorf("%w: нет тела запроса", errBadRequest)
	}
	v, err := h.svc.Find(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	p, err := h.sourceOperator(ctx, v.Obligation, auth.OpReplaceUnit)
	if err != nil {
		return nil, err
	}
	res, err := h.svc.Replace(ctx, app.ReplaceUnit{
		ObligationID: req.Id, OldUnitID: req.Body.OldUnitId, NewUnitID: req.Body.NewUnitId, Operator: p.Username,
	})
	if err != nil {
		return nil, err
	}
	return replacementResponse(res)
}

// CancelObligation — POST /obligations/{id}/cancel (служба или дежурный).
func (h *Handler) CancelObligation(ctx context.Context, req gen.CancelObligationRequestObject) (gen.CancelObligationResponseObject, error) {
	if req.Body == nil || !req.Body.Reason.Valid() {
		return nil, fmt.Errorf("%w: нужна причина отмены из перечисления", errBadRequest)
	}
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	res, err := h.svc.Cancel(ctx, nil, app.CancelObligation{ObligationID: req.Id, Reason: string(req.Body.Reason), Actor: p.Username})
	if err != nil {
		return nil, err
	}
	return gen.CancelObligation200JSONResponse(gen.CancelResult{
		ObligationId: res.ObligationID, Cancelled: res.Cancelled, Status: gen.ObligationStatus(res.Status),
	}), nil
}
