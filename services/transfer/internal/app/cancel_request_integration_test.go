//go:build integration

package app_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

func TestPlanRecalculationKeepsConfirmedAndSuppressesLateOpen(t *testing.T) {
	h := setup(t)
	units := h.registerUnits(1, "RBC", 4)
	n := newObligation(1, "RBC", 1, nil, domain.ReplacementSpec{RuleVersion: rule})
	n.Origin = domain.OriginPlanProposal
	confirmed := h.openWith(n)
	if res := h.confirm(confirmed, units[:1], false); !isType[domain.Confirmed](res) {
		t.Fatalf("подтверждение: %#v", res)
	}
	n.ChoiceID = uuid.New()
	pending := h.openWith(n)
	msg := delivery()
	c := transfer.CancelRequestObligations{RequestID: n.RequestID, Reason: transfer.CancelPlanRecalculated, OnlyPending: true}
	for range 2 {
		if err := h.svc.CancelRequest(h.ctx, msg, c); err != nil {
			t.Fatal(err)
		}
	}
	var status string
	if err := h.pool.QueryRow(h.ctx, `SELECT status FROM obligation WHERE obligation_id=$1`, pending).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "CANCELLED" {
		t.Fatalf("старое задание: %s", status)
	}
	if err := h.pool.QueryRow(h.ctx, `SELECT status FROM obligation WHERE obligation_id=$1`, confirmed).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "CONFIRMED" {
		t.Fatalf("подтверждённая передача отменена: %s", status)
	}
	n.ChoiceID = uuid.New()
	if id, err := h.svc.Open(h.ctx, delivery(), n); err != nil || id != uuid.Nil {
		t.Fatalf("позднее открытие: %s %v", id, err)
	}
	// Отмена может прийти даже раньше первого открытия.
	n.RequestID = uuid.New()
	if err := h.svc.CancelRequest(h.ctx, delivery(), transfer.CancelRequestObligations{RequestID: n.RequestID, Reason: transfer.CancelPlanRecalculated, OnlyPending: true}); err != nil {
		t.Fatal(err)
	}
	if id, err := h.svc.Open(h.ctx, delivery(), n); err != nil || id != uuid.Nil {
		t.Fatalf("открытие после надгробия: %s %v", id, err)
	}
	h.assertStockConsistent(1, "RBC")
}
