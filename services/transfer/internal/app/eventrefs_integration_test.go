//go:build integration

package app_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
)

// События transfer несут версию O07 после перехода и клиники обязательства (необязательные поля контрактов):
// проекции отбрасывают устаревшие события по версии, web фильтрует живые статусы по клинике.
func TestEventsCarryObligationVersionAndClinics(t *testing.T) {
	h := setup(t)
	unit := h.registerUnits(1, "RBC", 1)[0]
	o := h.open(1, "RBC", 1)
	h.confirm(o, []uuid.UUID{unit}, false)
	h.reply(app.LogisticsReply{Kind: app.ReplyAccepted, ObligationID: o, ShipmentRef: "c03-1"})
	h.cancel(o)

	cases := []struct{ event, version string }{
		{"ObligationOpened", "0"},
		{"TransferConfirmed", "1"},
		{"TransportRequested", "2"},
		{"TransferCancelled", "3"},
	}
	for _, c := range cases {
		if v := h.payloadField(o, c.event, "obligation_version"); v != c.version {
			t.Errorf("%s.obligation_version = %q, ожидали %s", c.event, v, c.version)
		}
	}
	for _, ev := range []string{"TransferConfirmed", "TransportRequested", "TransferCancelled"} {
		if src, tgt := h.payloadField(o, ev, "source_clinic_id"), h.payloadField(o, ev, "target_clinic_id"); src != "1" || tgt != "100" {
			t.Errorf("%s: клиники %q → %q, ожидали 1 → 100", ev, src, tgt)
		}
	}
	if v := h.view(o).Version; v != 3 {
		t.Errorf("версия обязательства %d, а в последнем событии 3", v)
	}
}
