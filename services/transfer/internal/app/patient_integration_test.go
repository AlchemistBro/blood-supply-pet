//go:build integration

package app_test

import (
	"encoding/json"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"
	"github.com/AlchemistBro/blood-supply-pet/libs/k1seal"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

func TestPatientEnvelopeTravelsOnlyInCommandAndOldAckCannotConfirmReplacement(t *testing.T) {
	h := setup(t)
	units := h.registerUnits(1, "RBC", 2)
	sealed, err := k1seal.Compose("test", make([]byte, k1seal.Overhead+1))
	if err != nil {
		t.Fatal(err)
	}
	n := newObligation(1, "RBC", 1, nil, domain.ReplacementSpec{RuleVersion: rule})
	n.PatientCaseSealed = sealed
	id := h.openWith(n)
	if res := h.confirm(id, units[:1], false); !isType[domain.Confirmed](res) {
		t.Fatalf("подтверждение: %#v", res)
	}
	var raw []byte
	if err := h.pool.QueryRow(h.ctx, `SELECT payload FROM outbox WHERE message_type='AllocateUnitsToPatient'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var command inventory.AllocateUnitsToPatient
	if err := json.Unmarshal(raw, &command); err != nil {
		t.Fatal(err)
	}
	if command.PatientCaseSealed != string(sealed) || command.RequestID != n.RequestID {
		t.Fatal("конверт потерян или привязан к чужой заявке")
	}
	if err := h.svc.OnPatientAllocationAcknowledged(h.ctx, delivery(), inventory.PatientAllocationAcknowledged{ObligationID: id, IdempotencyKey: command.IdempotencyKey, AcknowledgedAt: h.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	if res := h.replace(id, units[0], units[1]); !isType[domain.Replaced](res) {
		t.Fatalf("замена: %#v", res)
	}
	if err := h.svc.OnPatientAllocationAcknowledged(h.ctx, delivery(), inventory.PatientAllocationAcknowledged{ObligationID: id, IdempotencyKey: command.IdempotencyKey, AcknowledgedAt: h.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := h.pool.QueryRow(h.ctx, `SELECT patient_allocation_status FROM obligation WHERE obligation_id=$1`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "PENDING" {
		t.Fatalf("старая квитанция подтвердила новую единицу: %s", status)
	}
	var leaks int
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM outbox WHERE topic='transfer.events' AND payload::text LIKE '%patient_case_sealed%'`).Scan(&leaks); err != nil {
		t.Fatal(err)
	}
	if leaks != 0 {
		t.Fatal("конверт попал в событие transfer")
	}
	var commands int
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM outbox WHERE topic='inventory.commands'`).Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if commands != 3 {
		t.Fatalf("ожидали allocate, release, allocate; команд %d", commands)
	}
}
