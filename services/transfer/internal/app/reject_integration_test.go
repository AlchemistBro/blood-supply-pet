//go:build integration

package app_test

import (
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

func TestSourceRefusalIsIdempotentAndAudited(t *testing.T) {
	h := setup(t)
	o := h.open(1, "RBC", 1)
	for range 2 {
		result, err := h.svc.Reject(h.ctx, o, "operator-c001")
		if err != nil || !result {
			t.Fatalf("отказ: %v, %v", result, err)
		}
	}
	if h.view(o).Status != domain.StatusRejected {
		t.Fatal("задание не отклонено")
	}
	h.expectTypes("события", h.eventTypes(o), "ObligationOpened", "TransferRejected")
	items, err := h.svc.History(h.ctx, o, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Action != "REJECTED" {
		t.Fatalf("история отказа: %+v", items)
	}
}
