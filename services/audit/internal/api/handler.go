// Package api — HTTP-обработчики сервиса аудита по контракту api/openapi.yaml (строгий сервер oapi-codegen).
package api

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/audit/internal/api/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/audit/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/audit/internal/domain"
)

// Handler реализует gen.StrictServerInterface.
type Handler struct {
	svc *app.Service
}

// Mount регистрирует маршруты на mux с проверкой прав по операции.
func Mount(mux *http.ServeMux, svc *app.Service, onDenied auth.Denied) {
	h := &Handler{svc: svc}
	strict := gen.NewStrictHandlerWithOptions(h, nil, gen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			httpx.WriteProblem(w, r, http.StatusBadRequest, "Неверный запрос", err.Error())
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
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

// authorize выбирает операцию матрицы прав по шаблону маршрута (r.Pattern заполняет ServeMux).
func authorize(onDenied auth.Denied) gen.MiddlewareFunc {
	ops := map[string]auth.Operation{
		"GET /records":         auth.OpReadAudit,
		"POST /records":        auth.OpWriteAudit,
		"GET /internal/verify": auth.OpReadAudit,
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

// ListRecords — GET /records.
func (h *Handler) ListRecords(ctx context.Context, req gen.ListRecordsRequestObject) (gen.ListRecordsResponseObject, error) {
	f := app.Filter{
		Module: req.Params.Module, Action: req.Params.Action, ObjectType: req.Params.ObjectType,
		ObjectID: req.Params.ObjectId, UserID: req.Params.UserId, From: req.Params.From, To: req.Params.To,
	}
	if req.Params.AfterSeq != nil {
		f.AfterSeq = *req.Params.AfterSeq
	}
	if req.Params.Limit != nil {
		f.Limit = *req.Params.Limit
	}
	page, err := h.svc.List(ctx, f)
	if err != nil {
		return nil, err
	}
	out := gen.RecordPage{Items: make([]gen.AuditRecord, 0, len(page.Items)), NextAfterSeq: page.NextAfterSeq}
	for _, r := range page.Items {
		out.Items = append(out.Items, toAPI(r))
	}
	return gen.ListRecords200JSONResponse(out), nil
}

// AppendRecord — POST /records (от web).
func (h *Handler) AppendRecord(ctx context.Context, req gen.AppendRecordRequestObject) (gen.AppendRecordResponseObject, error) {
	if req.Body == nil {
		return gen.AppendRecord400ApplicationProblemPlusJSONResponse{Title: "Неверный запрос", Status: 400, Detail: ptr("Пустое тело")}, nil
	}
	p, _ := auth.FromContext(ctx)
	producer := p.Username
	if producer == "" {
		producer = "web"
	}
	in := req.Body
	rec := auditc.AuditRecorded{
		OccurredAt: in.OccurredAt, UserID: deref(in.UserId), Role: deref(in.Role), ClinicID: in.ClinicId,
		Module: string(in.Module), Action: in.Action, ObjectType: in.ObjectType, ObjectID: in.ObjectId,
		Result: string(in.Result), CorrelationID: deref(in.CorrelationId),
	}
	if in.Details != nil {
		rec.Details = *in.Details
	}
	stored, err := h.svc.AppendDirect(ctx, rec, producer)
	if err != nil {
		if errors.Is(err, app.ErrInvalid) {
			return gen.AppendRecord400ApplicationProblemPlusJSONResponse{Title: "Неверная запись аудита", Status: 400, Detail: ptr(err.Error())}, nil
		}
		return nil, err
	}
	return gen.AppendRecord201JSONResponse{Seq: stored.Seq, Hash: hex.EncodeToString(stored.Hash)}, nil
}

// VerifyChain — GET /internal/verify.
func (h *Handler) VerifyChain(ctx context.Context, _ gen.VerifyChainRequestObject) (gen.VerifyChainResponseObject, error) {
	rep, err := h.svc.Verify(ctx)
	if err != nil {
		return nil, err
	}
	out := gen.VerifyResult{Ok: rep.OK, Checked: rep.Checked, HeadHash: ptr(hex.EncodeToString(rep.HeadHash))}
	if rep.Broken != nil {
		out.Broken = &struct {
			Reason string `json:"reason"`
			Seq    int64  `json:"seq"`
		}{Reason: rep.Broken.Reason, Seq: rep.Broken.Seq}
	}
	return gen.VerifyChain200JSONResponse(out), nil
}

func toAPI(r domain.Record) gen.AuditRecord {
	out := gen.AuditRecord{
		Id: openapi_types.UUID(r.ID), Seq: r.Seq, OccurredAt: r.OccurredAt, ReceivedAt: r.ReceivedAt,
		Module: gen.AuditRecordModule(r.Module), Action: r.Action, ObjectType: r.ObjectType, ObjectId: r.ObjectID,
		Result: gen.AuditRecordResult(r.Result), Producer: r.Producer, Hash: hex.EncodeToString(r.Hash),
		ClinicId: r.ClinicID, UserId: nullable(r.UserID), Role: nullable(r.Role), CorrelationId: nullable(r.CorrelationID),
	}
	if r.Details != nil {
		d := r.Details
		out.Details = &d
	}
	return out
}

func ptr[T any](v T) *T { return &v }

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
