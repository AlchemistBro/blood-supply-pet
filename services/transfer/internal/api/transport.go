package api

import (
	"context"
	"fmt"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/api/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

func (h *Handler) ListEscalations(ctx context.Context, req gen.ListEscalationsRequestObject) (gen.ListEscalationsResponseObject, error) {
	includeClosed := req.Params.IncludeClosed != nil && *req.Params.IncludeClosed
	items, err := h.svc.Escalations(ctx, includeClosed)
	if err != nil {
		return nil, err
	}
	out := make(gen.ListEscalations200JSONResponse, 0, len(items))
	for _, n := range items {
		out = append(out, gen.Escalation{EscalationId: n.ID, ObligationId: n.ObligationID, Reason: n.Reason, RaisedAt: n.RaisedAt, ClosedAt: n.ClosedAt, ClosedBy: n.ClosedBy, Resolution: n.Resolution, Comment: n.Comment})
	}
	return out, nil
}
func (h *Handler) CloseEscalation(ctx context.Context, req gen.CloseEscalationRequestObject) (gen.CloseEscalationResponseObject, error) {
	if req.Body == nil {
		return nil, errBadRequest
	}
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.CloseEscalation(ctx, req.Id, p.Username, string(req.Body.Resolution), req.Body.Comment); err != nil {
		return nil, err
	}
	return gen.CloseEscalation204Response{}, nil
}
func (h *Handler) ListAllObligations(ctx context.Context, req gen.ListAllObligationsRequestObject) (gen.ListAllObligationsResponseObject, error) {
	var status *domain.Status
	if req.Params.Status != nil && *req.Params.Status != "" {
		value, err := domain.ParseStatus(*req.Params.Status)
		if err != nil {
			return nil, fmt.Errorf("%w: неверный статус", errBadRequest)
		}
		status = &value
	}
	offset := 0
	if req.Params.Offset != nil {
		offset = *req.Params.Offset
	}
	if offset < 0 {
		return nil, errBadRequest
	}
	items, err := h.svc.All(ctx, status, offset)
	if err != nil {
		return nil, err
	}
	out := gen.ObligationList{Items: make([]gen.Obligation, 0, len(items))}
	for _, n := range items {
		out.Items = append(out.Items, toObligation(n))
	}
	return gen.ListAllObligations200JSONResponse(out), nil
}
func (h *Handler) AssignManualTransport(ctx context.Context, req gen.AssignManualTransportRequestObject) (gen.AssignManualTransportResponseObject, error) {
	if req.Body == nil {
		return nil, errBadRequest
	}
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.ManualTransport(ctx, req.Id, req.Body.TransportRef, req.Body.CourierRef, p.Username); err != nil {
		return nil, err
	}
	return gen.AssignManualTransport204Response{}, nil
}
func (h *Handler) GetTransport(ctx context.Context, req gen.GetTransportRequestObject) (gen.GetTransportResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	o, err := h.svc.Find(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if !visible(p, o.Obligation) {
		return nil, deniedError{op: auth.OpViewObligation}
	}
	n, err := h.svc.Transport(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	violations := n.Violations
	if violations == nil {
		violations = []string{}
	}
	return gen.GetTransport200JSONResponse{ObligationId: n.ObligationID, TransportRef: n.TransportRef, CourierRef: n.CourierRef, Manual: n.Manual, AssignedBy: n.AssignedBy, AssignedAt: n.AssignedAt, DepartedAt: n.DepartedAt, ArrivedAt: n.ArrivedAt, TemperatureAtDispatchC: n.DepartureTemperature, TemperatureAtArrivalC: n.ArrivalTemperature, DepartureContainerIntact: n.DepartureContainerIntact, ArrivalContainerIntact: n.ArrivalContainerIntact, ConditionsViolated: n.ConditionsViolated, Violations: violations}, nil
}
func (h *Handler) DispatchTransfer(ctx context.Context, req gen.DispatchTransferRequestObject) (gen.DispatchTransferResponseObject, error) {
	if req.Body == nil {
		return nil, errBadRequest
	}
	o, err := h.svc.Find(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	p, err := h.sourceOperator(ctx, o.Obligation, auth.OpDispatchTransfer)
	if err != nil {
		return nil, err
	}
	at := time.Time{}
	if req.Body.At != nil {
		at = *req.Body.At
	}
	if err := h.svc.Dispatch(ctx, nil, app.DispatchInput{ObligationID: req.Id, CourierRef: req.Body.CourierRef, Actor: p.Username, Role: p.PrimaryRole(), At: at, Temperature: req.Body.TemperatureC, ContainerIntact: req.Body.ContainerIntact}); err != nil {
		return nil, err
	}
	return gen.DispatchTransfer204Response{}, nil
}
func (h *Handler) ReceiveTransfer(ctx context.Context, req gen.ReceiveTransferRequestObject) (gen.ReceiveTransferResponseObject, error) {
	if req.Body == nil {
		return nil, errBadRequest
	}
	o, err := h.svc.Find(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if !auth.SameClinic(p, o.TargetClinicID) {
		return nil, deniedError{op: auth.OpReceiveTransfer}
	}
	at := time.Time{}
	if req.Body.At != nil {
		at = *req.Body.At
	}
	if err := h.svc.Receive(ctx, nil, app.ReceiveInput{ObligationID: req.Id, Actor: p.Username, Role: p.PrimaryRole(), At: at, Temperature: req.Body.TemperatureC, ContainerIntact: req.Body.ContainerIntact, TemperatureExcursion: req.Body.TemperatureExcursion}); err != nil {
		return nil, err
	}
	return gen.ReceiveTransfer204Response{}, nil
}
