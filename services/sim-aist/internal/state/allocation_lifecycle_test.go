package state_test

import (
	"errors"
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/services/sim-aist/internal/state"
)

func TestReleasedAllocationCannotBeRevivedByLateReplay(t *testing.T) {
	s, _, ref := newStore(t)
	unit := mustCreate(t, s, newUnit(ref, "R-1"))
	req := state.AllocationRequest{IdempotencyKey: "key-1", PatientCaseNo: "CASE", RecipientClinicCode: ref.Clinics[0].ClinicCode, AistUnitCodes: []string{unit.AistUnitCode}}
	if _, created, err := s.Allocate(req); err != nil || !created {
		t.Fatal("закрепление не принято")
	}
	if err := s.Release(req.IdempotencyKey); err != nil {
		t.Fatal(err)
	}
	if replay, created, err := s.Allocate(req); err != nil || created || !replay.Released {
		t.Fatal("поздний POST оживил снятое закрепление")
	}
	req.IdempotencyKey = "key-2"
	if _, created, err := s.Allocate(req); err != nil || !created {
		t.Fatal("снятие не освободило единицу")
	}
	if err := s.Release("key-before-post"); err != nil {
		t.Fatal(err)
	}
	req.IdempotencyKey = "key-before-post"
	if _, _, err := s.Allocate(req); !errors.Is(err, state.ErrInvalid) {
		t.Fatal("снятие до POST не сохранило надгробие")
	}
}
func TestExpiryProducesObservableWithdrawalAndRejectsAllocation(t *testing.T) {
	s, clk, ref := newStore(t)
	input := newUnit(ref, "EXP-1")
	input.ExpiresAt = t0.Add(time.Hour)
	unit := mustCreate(t, s, input)
	clk.Advance(time.Hour)
	changes, _, _ := s.Changes(unit.Version, 100)
	if len(changes) != 1 || changes[0].Status != state.StatusDiscarded || changes[0].WithdrawalReason != state.ReasonExpired {
		t.Fatalf("истечение не видно в ИНТ-01: %+v", changes)
	}
	if _, _, err := s.Allocate(state.AllocationRequest{IdempotencyKey: "expired", PatientCaseNo: "CASE", RecipientClinicCode: input.ClinicCode, AistUnitCodes: []string{input.AistUnitCode}}); !errors.Is(err, state.ErrUnitUnavailable) {
		t.Fatal("истёкшая единица закреплена")
	}
}
