package api

import (
	"context"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/api/gen"
)

func (h *Handler) GetCandidates(ctx context.Context, req gen.GetCandidatesRequestObject) (gen.GetCandidatesResponseObject, error) {
	v, err := h.svc.Find(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if _, err := h.sourceOperator(ctx, v.Obligation, auth.OpConfirmTransfer); err != nil {
		return nil, err
	}
	units, err := h.svc.Candidates(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	out := gen.GetCandidates200JSONResponse{Units: make([]gen.Candidate, 0, len(units))}
	for _, u := range units {
		out.Units = append(out.Units, gen.Candidate{UnitId: u.UnitID, ClinicId: u.ClinicID, ComponentKind: u.ComponentKind, BloodGroup: u.BloodGroup, VolumeMl: u.VolumeML, ExpiresAt: u.ExpiresAt, Status: string(u.Status), Available: true})
	}
	return out, nil
}
