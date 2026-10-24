package api

import (
	"context"
	"fmt"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/api/gen"
)

func (h *Handler) GetObligationHistory(ctx context.Context, req gen.GetObligationHistoryRequestObject) (gen.GetObligationHistoryResponseObject, error) {
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
	limit, offset := 50, 0
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	if req.Params.Offset != nil {
		offset = *req.Params.Offset
	}
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, fmt.Errorf("%w: неверная страница истории", errBadRequest)
	}
	items, err := h.svc.History(ctx, req.Id, limit, offset)
	if err != nil {
		return nil, err
	}
	out := gen.GetObligationHistory200JSONResponse{Items: make([]gen.HistoryEntry, 0, len(items))}
	for _, item := range items {
		out.Items = append(out.Items, gen.HistoryEntry{Id: item.ID, OccurredAt: item.At, Action: item.Action, Result: item.Result})
	}
	return out, nil
}

func (h *Handler) RejectObligation(ctx context.Context, req gen.RejectObligationRequestObject) (gen.RejectObligationResponseObject, error) {
	if req.Body == nil || !req.Body.Reason.Valid() {
		return nil, fmt.Errorf("%w: нужна причина SOURCE_REFUSED", errBadRequest)
	}
	v, err := h.svc.Find(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	p, err := h.sourceOperator(ctx, v.Obligation, auth.OpRejectTransfer)
	if err != nil {
		return nil, err
	}
	rejected, err := h.svc.Reject(ctx, req.Id, p.Username)
	if err != nil {
		return nil, err
	}
	message := "Задание уже обработано. Отказ не выполнен."
	if rejected {
		message = "Задание отклонено: клиника-источник отказала в передаче."
	}
	return gen.RejectObligation200JSONResponse{Rejected: rejected, Message: message}, nil
}
