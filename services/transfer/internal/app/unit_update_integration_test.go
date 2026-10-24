//go:build integration

package app_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

func TestReducedExpiryBreaksConfirmedSpecificationAndStaleUpdateCannotUndoIt(t *testing.T) {
	h := setup(t)
	units := h.registerUnits(1, "RBC", 1)
	minimum := h.inDays(10)
	id := h.openWith(newObligation(1, "RBC", 1, nil, domain.ReplacementSpec{RuleVersion: rule, MinExpiresAt: &minimum}))
	h.confirm(id, units, false)
	shortened := h.inDays(5)
	version := h.versions.Add(1)
	if err := h.svc.OnUnitUpdated(h.ctx, delivery(), app.UnitUpdated{UnitID: units[0], ClinicID: 1, ComponentKind: "RBC", Status: domain.UnitInStock, ExpiresAt: &shortened, Version: version}); err != nil {
		t.Fatal(err)
	}
	v, err := h.svc.Find(h.ctx, id)
	if err != nil || v.Status != domain.StatusBroken {
		t.Fatalf("сокращение годности не нарушило спецификацию: %+v %v", v, err)
	}
	longer := h.inDays(30)
	if err := h.svc.OnUnitUpdated(h.ctx, delivery(), app.UnitUpdated{UnitID: units[0], ClinicID: target, ComponentKind: "RBC", Status: domain.UnitInStock, ExpiresAt: &longer, Version: version - 1}); err != nil {
		t.Fatal(err)
	}
	var clinic int64
	var expires time.Time
	if err := h.pool.QueryRow(h.ctx, `SELECT clinic_id,expires_at FROM unit_copy WHERE unit_id=$1`, units[0]).Scan(&clinic, &expires); err != nil {
		t.Fatal(err)
	}
	if clinic != 1 || !expires.Equal(shortened) {
		t.Fatal("устаревшее сообщение изменило срок или клинику")
	}
	h.assertStockConsistent(1, "RBC")
}

func TestCancellationReportsAlreadyDispatchedAndClinicMoveClearsLocalTransit(t *testing.T) {
	h := setup(t)
	units := h.registerUnits(1, "RBC", 1)
	id := h.open(1, "RBC", 1)
	h.confirm(id, units, false)
	h.reply(app.LogisticsReply{Kind: app.ReplyAccepted, ObligationID: id, ShipmentRef: "C03-1"})
	temp := 4.0
	if err := h.svc.Dispatch(h.ctx, nil, app.DispatchInput{ObligationID: id, CourierRef: "courier-1", Actor: "operator", Temperature: &temp, ContainerIntact: true}); err != nil {
		t.Fatal(err)
	}
	v, err := h.svc.Find(h.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.CancelRequest(h.ctx, delivery(), transfer.CancelRequestObligations{RequestID: v.RequestID, Reason: transfer.CancelRequestCancelled}); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := h.pool.QueryRow(h.ctx, `SELECT payload FROM outbox WHERE message_type='RequestCancellationProcessed'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var reply transfer.RequestCancellationProcessed
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.NonCancellableObligationIDs) != 1 || reply.NonCancellableObligationIDs[0] != id {
		t.Fatal("отправленная часть не указана в результате отмены")
	}
	if err := h.svc.OnUnitUpdated(h.ctx, delivery(), app.UnitUpdated{UnitID: units[0], ClinicID: target, ComponentKind: "RBC", Status: domain.UnitInStock, Version: h.versions.Add(1)}); err != nil {
		t.Fatal(err)
	}
	var cleared bool
	if err := h.pool.QueryRow(h.ctx, `SELECT local_dispatch_obligation_id IS NULL FROM unit_copy WHERE unit_id=$1 AND clinic_id=$2`, units[0], target).Scan(&cleared); err != nil {
		t.Fatal(err)
	}
	if !cleared {
		t.Fatal("учтённая приёмка не сняла локальный признак отправки")
	}
	h.assertStockConsistent(1, "RBC")
	h.assertStockConsistent(target, "RBC")
}
