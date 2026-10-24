//go:build integration

package app_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/domain"
)

func TestRemoteCancellationWaitsForSourceAndLateDispatchPreventsFalseCancellation(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "отменена", true: "уже отправлена"}[blocked], func(t *testing.T) {
			h := setup(t)
			actor := app.Actor{User: "doctor-1", ClinicID: 1, Roles: []string{"PHYSICIAN"}}
			request := domain.Request{RequestID: uuid.New(), ClinicID: 1, ComponentKind: "RBC", RequestedUnits: 1, Urgency: "PLANNED", BloodGroup: "O+", Phenotype: "K-"}
			n, err := h.svc.Create(h.ctx, request, "DEMO_CASE", actor)
			if err != nil || len(n.Result.Variants) == 0 {
				t.Fatalf("заявка: %v", err)
			}
			if _, err := h.svc.Select(h.ctx, request.RequestID, app.VariantID(request.RequestID, 1), uuid.New(), actor); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := h.svc.Cancel(h.ctx, request.RequestID, actor); err != nil {
					t.Fatal(err)
				}
			}
			n, err = h.svc.Request(h.ctx, request.RequestID, actor)
			if err != nil || n.Status != "CANCELLATION_REQUESTED" {
				t.Fatalf("отмена объявлена до источника: %s %v", n.Status, err)
			}
			var count int
			if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM outbox WHERE message_type='ClinicalRequestCancelled'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("событие окончательной отмены опубликовано преждевременно")
			}
			ids := make([]uuid.UUID, 0)
			if blocked {
				id := uuid.New()
				ids = append(ids, id)
				version := int64(1)
				h.send(t, transfer.TransferDispatched{ObligationID: id, ObligationVersion: &version, RequestID: request.RequestID, At: h.at})
			}
			reply := transfer.RequestCancellationProcessed{RequestID: request.RequestID, NonCancellableObligationIDs: ids, At: h.at}
			h.send(t, reply)
			h.send(t, reply)
			n, err = h.svc.Request(h.ctx, request.RequestID, actor)
			if err != nil {
				t.Fatal(err)
			}
			expected := "CANCELLED"
			if blocked {
				expected = "CANCELLATION_REJECTED"
			}
			if n.Status != expected {
				t.Fatalf("результат источника потерян: %s", n.Status)
			}
			if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM outbox WHERE message_type='ClinicalRequestCancelled'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 1
			if blocked {
				want = 0
			}
			if count != want {
				t.Fatalf("неверное число окончательных отмен: %d", count)
			}
		})
	}
}
