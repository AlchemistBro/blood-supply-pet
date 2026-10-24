package api

import (
	"context"
	"net/http"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/api/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/app"
)

type Handler struct {
	Read func(context.Context) ([]app.TransportStatus, error)
}

func Mount(mux *http.ServeMux, read func(context.Context) ([]app.TransportStatus, error), denied auth.Denied) {
	strict := gen.NewStrictHandlerWithOptions(&Handler{Read: read}, nil, gen.StrictHTTPServerOptions{ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
		httpx.WriteProblem(w, r, 503, "Шлюз недоступен", "Не удалось прочитать состояние заказов.")
	}})
	gen.HandlerWithOptions(strict, gen.StdHTTPServerOptions{BaseRouter: mux, Middlewares: []gen.MiddlewareFunc{func(next http.Handler) http.Handler { return auth.Require(auth.OpReadAudit, denied)(next) }}})
}
func (h *Handler) TransportStatus(ctx context.Context, _ gen.TransportStatusRequestObject) (gen.TransportStatusResponseObject, error) {
	rows, err := h.Read(ctx)
	if err != nil {
		return nil, err
	}
	out := make(gen.TransportStatus200JSONResponse, 0, len(rows))
	for _, v := range rows {
		out = append(out, gen.TransportStatus{ObligationId: v.ID, Status: v.Status, ShipmentRef: v.ShipmentRef, LastError: v.LastError, UpdatedAt: v.UpdatedAt, DispatchReported: v.DispatchReported, DeliveryReported: v.DeliveryReported})
	}
	return out, nil
}
