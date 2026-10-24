package domain_test

import (
	"slices"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

// Закреплять можно только единицу IN_STOCK. Карантин — никогда (ПП РФ № 641, п. 62 а).
func TestOnlyInStockUnitIsAllocatable(t *testing.T) {
	want := map[domain.UnitStatus]bool{domain.UnitInStock: true}
	for _, s := range domain.AllUnitStatuses {
		if s.Allocatable() != want[s] {
			t.Errorf("%s: закрепляемая=%v, ожидали %v", s, s.Allocatable(), want[s])
		}
	}
	if domain.UnitStatus("UNKNOWN").Allocatable() {
		t.Error("неизвестный статус не должен считаться доступным")
	}
}

// Причина выбытия переводится в код справочника ref_unit_status; EXPIRED — списание.
func TestWithdrawalStatus(t *testing.T) {
	cases := map[string]domain.UnitStatus{
		"ISSUED": domain.UnitIssued, "DISCARDED": domain.UnitDiscarded, "EXPIRED": domain.UnitDiscarded,
		"TRANSFERRED": domain.UnitTransferred,
	}
	for reason, want := range cases {
		got, err := domain.WithdrawalStatus(reason)
		if err != nil || got != want {
			t.Errorf("WithdrawalStatus(%s) = %s, %v; ожидали %s", reason, got, err, want)
		}
		if got.Allocatable() {
			t.Errorf("%s: выбывшая единица не может быть доступна", reason)
		}
	}
	if _, err := domain.WithdrawalStatus("TRANSFUSED"); err == nil {
		t.Error("неизвестная причина выбытия должна давать ошибку")
	}
}

func TestParseUnitStatus(t *testing.T) {
	for _, s := range domain.AllUnitStatuses {
		got, err := domain.ParseUnitStatus(string(s))
		if err != nil || got != s {
			t.Errorf("ParseUnitStatus(%q) = %q, %v", s, got, err)
		}
	}
	if _, err := domain.ParseUnitStatus("WITHDRAWN"); err == nil {
		t.Error("WITHDRAWN — статус копии transfer, а не единицы inventory")
	}
}

// Причина нарушения зависит от нового статуса: карантин отличается от выбытия.
func TestBreakReasonForUnitStatus(t *testing.T) {
	if r, ok := domain.BreakReasonFor(domain.UnitQuarantine); !ok || r != domain.BreakUnitQuarantined {
		t.Errorf("карантин: %s, %v", r, ok)
	}
	for _, s := range []domain.UnitStatus{domain.UnitIssued, domain.UnitDiscarded, domain.UnitTransferred} {
		if r, ok := domain.BreakReasonFor(s); !ok || r != domain.BreakUnitWithdrawn {
			t.Errorf("выбытие %s: %s, %v", s, r, ok)
		}
	}
	if _, ok := domain.BreakReasonFor(domain.UnitInStock); ok {
		t.Error("единица в наличии не нарушает обязательство")
	}
}

// Правило «закреплять только IN_STOCK» совпадает со справочником ref_unit_status (is_available).
func TestAllocatableMatchesUnitStatusDictionary(t *testing.T) {
	ref := refdata.MustLoad()
	if len(ref.UnitStatuses) != len(domain.AllUnitStatuses) {
		t.Fatalf("в справочнике %d статусов единицы, в домене %d", len(ref.UnitStatuses), len(domain.AllUnitStatuses))
	}
	for _, s := range domain.AllUnitStatuses {
		if s.Allocatable() != ref.IsUnitAvailable(string(s)) {
			t.Errorf("%s: домен %v, справочник %v", s, s.Allocatable(), ref.IsUnitAvailable(string(s)))
		}
	}
}

// Причина CancelTransport берётся из перечисления контракта logistics, своих кодов нет.
func TestCancelTransportReasonIsContractValue(t *testing.T) {
	for _, r := range []domain.BreakReason{domain.BreakUnitWithdrawn, domain.BreakUnitQuarantined} {
		if !slices.Contains(logistics.CancelReasons, r.CancelTransportReason()) {
			t.Errorf("%s → %q вне перечисления CancelTransport.reason", r, r.CancelTransportReason())
		}
	}
	if domain.BreakUnitQuarantined.CancelTransportReason() != logistics.CancelUnitQuarantined {
		t.Error("карантин снимает заказ с причиной UNIT_QUARANTINED")
	}
	if r := domain.BrokenEventReason(domain.BreakUnitQuarantined, "QUARANTINE"); r != transfer.BrokenQuarantine {
		t.Errorf("TransferBroken.reason при карантине %q", r)
	}
	if r := domain.BrokenEventReason(domain.BreakUnitWithdrawn, "EXPIRED"); !slices.Contains(transfer.BrokenReasons, r) {
		t.Errorf("TransferBroken.reason при выбытии %q вне перечисления контракта", r)
	}
	for _, s := range []domain.UnitStatus{domain.UnitIssued, domain.UnitDiscarded, domain.UnitTransferred} {
		if !slices.Contains(transfer.BrokenReasons, string(s)) {
			t.Errorf("статус выбытия %s вне TransferBroken.reason", s)
		}
	}
	if string(domain.BreakUnitWithdrawn) != logistics.CancelUnitWithdrawn {
		t.Errorf("UNIT_WITHDRAWN расходится с контрактом: %s", domain.BreakUnitWithdrawn)
	}
}
