package api

import (
	"net/http"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-logistics/internal/carrier"
)

func (h *handler) setProgress(w http.ResponseWriter, r *http.Request) {
	var p carrier.Progress
	if err := decodeStrict(w, r, &p); err != nil {
		httpx.WriteProblem(w, r, 400, "Неверные факты перевозки", err.Error())
		return
	}
	if err := h.carrier.SetProgress(r.PathValue("shipmentRef"), p); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
