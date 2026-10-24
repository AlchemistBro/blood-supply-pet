package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/api/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/app"
)

func (h *Handler) SeedDemoNorms(ctx context.Context, req gen.SeedDemoNormsRequestObject) (gen.SeedDemoNormsResponseObject, error) {
	if req.Body == nil {
		return gen.SeedDemoNorms400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: problem(http.StatusBadRequest, "Укажите год", "Нужен год синтетического расхода.")}, nil
	}
	p, _ := auth.FromContext(ctx)
	created, err := h.svc.SeedDemoNorms(ctx, app.Actor{UserID: p.Username, Role: p.PrimaryRole()}, req.Body.ConsumptionYear)
	if errors.Is(err, app.ErrInvalid) {
		return gen.SeedDemoNorms400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: problem(http.StatusBadRequest, "Неверный год", "Укажите год от 2000 до 2100.")}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.SeedDemoNorms200JSONResponse{Created: created}, nil
}
