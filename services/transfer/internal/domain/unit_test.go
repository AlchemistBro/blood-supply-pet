package domain_test

import (
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

// Закреплять можно только единицу IN_STOCK. Карантин — никогда (ПП РФ № 641, п. 62).
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

func TestUnitStatusMapsToCopyStatus(t *testing.T) {
	cases := map[domain.UnitStatus]domain.CopyStatus{
		domain.UnitInStock:     domain.CopyInStock,
		domain.UnitQuarantine:  domain.CopyQuarantine,
		domain.UnitIssued:      domain.CopyWithdrawn,
		domain.UnitDiscarded:   domain.CopyWithdrawn,
		domain.UnitTransferred: domain.CopyWithdrawn,
	}
	if len(cases) != len(domain.AllUnitStatuses) {
		t.Fatalf("таблица не покрывает все %d статусов единицы", len(domain.AllUnitStatuses))
	}
	for s, want := range cases {
		got, err := domain.CopyStatusOf(s)
		if err != nil || got != want {
			t.Errorf("CopyStatusOf(%s) = %s, %v; ожидали %s", s, got, err, want)
		}
	}
	if _, err := domain.CopyStatusOf("LOST"); err == nil {
		t.Error("неизвестный статус единицы должен давать ошибку")
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
	if r, ok := domain.BreakReasonFor(domain.CopyQuarantine); !ok || r != domain.BreakUnitQuarantined {
		t.Errorf("карантин: %s, %v", r, ok)
	}
	if r, ok := domain.BreakReasonFor(domain.CopyWithdrawn); !ok || r != domain.BreakUnitWithdrawn {
		t.Errorf("выбытие: %s, %v", r, ok)
	}
	if _, ok := domain.BreakReasonFor(domain.CopyInStock); ok {
		t.Error("единица в наличии не нарушает обязательство")
	}
}

func TestTemperatureRegimeForEveryKind(t *testing.T) {
	cases := map[string]string{
		"RBC":  domain.RegimeRefrigerated,
		"FFP":  domain.RegimeFrozen,
		"CRYO": domain.RegimeFrozen,
		"PLT":  domain.RegimeRoomAgitated,
	}
	for kind, want := range cases {
		got, err := domain.TemperatureRegime(kind)
		if err != nil || got != want {
			t.Errorf("TemperatureRegime(%s) = %s, %v; ожидали %s", kind, got, err, want)
		}
	}
	if _, err := domain.TemperatureRegime("WHOLE"); err == nil {
		t.Error("неизвестный вид компонента должен давать ошибку, а не режим по умолчанию")
	}
}
