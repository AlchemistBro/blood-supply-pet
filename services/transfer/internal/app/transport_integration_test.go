//go:build integration

package app_test

import (
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

func TestDispatchedUnitCannotBeConfirmedAgainAndViolationsStayBlocked(t *testing.T) {
	h := setup(t)
	units := h.registerUnits(1, "RBC", 2)
	id := h.open(1, "RBC", 1)
	h.confirm(id, units[:1], false)
	h.reply(app.LogisticsReply{Kind: app.ReplyAccepted, ObligationID: id, ShipmentRef: "C03-1"})
	departure := 4.0
	if err := h.svc.Dispatch(h.ctx, nil, app.DispatchInput{ObligationID: id, CourierRef: "courier-1", Actor: "operator", Temperature: &departure, ContainerIntact: true}); err != nil {
		t.Fatal(err)
	}
	h.assertStockConsistent(1, "RBC")
	other := h.open(1, "RBC", 1)
	if result := h.confirm(other, units[:1], false); isType[domain.Confirmed](result) {
		t.Fatal("отправленная единица закреплена повторно")
	}
	h.clock.Advance(time.Hour)
	arrival := 11.0
	if err := h.svc.Receive(h.ctx, nil, app.ReceiveInput{ObligationID: id, Actor: "receiver", Temperature: &arrival, ContainerIntact: true}); err != nil {
		t.Fatal(err)
	}
	escalations, err := h.svc.Escalations(h.ctx, false)
	if err != nil || len(escalations) != 1 || escalations[0].Reason != transfer.EscalationTemperatureExcursion {
		t.Fatalf("нет температурной эскалации: %+v %v", escalations, err)
	}
	if err := h.svc.CloseEscalation(h.ctx, escalations[0].ID, "duty", transfer.ResolutionResolved, "АИСТ-М передан запрет использования"); err != nil {
		t.Fatal(err)
	}
	v, err := h.svc.Find(h.ctx, id)
	if err != nil || !v.ClinicalUseBlocked || v.Status != domain.StatusDelivered || len(v.Units) != 1 {
		t.Fatalf("закрытие сняло запрет или потеряны единицы: %+v %v", v, err)
	}
}
func TestManualTransportClosesEscalationAndLateCarrierReplyKeepsManualRef(t *testing.T) {
	h := setup(t)
	units := h.registerUnits(1, "RBC", 1)
	id := h.open(1, "RBC", 1)
	h.confirm(id, units, false)
	h.reply(app.LogisticsReply{Kind: app.ReplyRejected, ObligationID: id, Reason: app.RejectRefused})
	if err := h.svc.ManualTransport(h.ctx, id, "MANUAL-1", "courier-1", "duty"); err != nil {
		t.Fatal(err)
	}
	h.reply(app.LogisticsReply{Kind: app.ReplyAccepted, ObligationID: id, ShipmentRef: "C03-LATE"})
	v, err := h.svc.Find(h.ctx, id)
	if err != nil || v.TransportRef != "MANUAL-1" || v.Status != domain.StatusTransportRequested {
		t.Fatalf("поздний ответ заменил ручную перевозку: %+v %v", v, err)
	}
	open, err := h.svc.Escalations(h.ctx, false)
	if err != nil || len(open) != 0 {
		t.Fatalf("эскалация осталась открытой: %+v %v", open, err)
	}
}
