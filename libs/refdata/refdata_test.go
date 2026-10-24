package refdata_test

import (
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
)

func TestLoadIsConsistent(t *testing.T) {
	s, err := refdata.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Clinics) != 100 || len(s.Districts) != 12 || len(s.ComponentKinds) != 4 || len(s.Urgency) != 3 {
		t.Fatalf("состав набора: клиник %d, районов %d, видов %d, срочностей %d",
			len(s.Clinics), len(s.Districts), len(s.ComponentKinds), len(s.Urgency))
	}
	if _, ok := s.Clinic(1); !ok {
		t.Fatal("клиника 1 не найдена")
	}
	if k, ok := s.Kind("CRYO"); !ok || k.IsGroupApplicable {
		t.Fatal("криопреципитат должен быть без признака группы")
	}
	if u, ok := s.UrgencyByCode("EMERGENCY"); !ok || u.ConfirmWindowMinutes != 15 {
		t.Fatalf("экстренная срочность: %+v %v", u, ok)
	}
}

// Для закрепления доступна только единица в наличии (решение проекта, ADR-0027); статус «на карантине»
// есть в базе по ПП РФ № 641, п. 62 а.
func TestUnitStatusesAvailability(t *testing.T) {
	s := refdata.MustLoad()
	want := map[string]bool{"IN_STOCK": true, "QUARANTINE": false, "ISSUED": false, "DISCARDED": false, "TRANSFERRED": false}
	if len(s.UnitStatuses) != len(want) {
		t.Fatalf("статусов единицы %d, ожидали %d", len(s.UnitStatuses), len(want))
	}
	for code, available := range want {
		st, ok := s.UnitStatus(code)
		if !ok {
			t.Errorf("статус %s не найден", code)
			continue
		}
		if st.IsAvailable != available || st.Name == "" {
			t.Errorf("статус %s: доступна=%v, имя %q; ожидали доступна=%v", code, st.IsAvailable, st.Name, available)
		}
		if s.IsUnitAvailable(code) != available {
			t.Errorf("IsUnitAvailable(%s) != %v", code, available)
		}
	}
	if s.IsUnitAvailable("UNKNOWN") {
		t.Error("неизвестный статус не должен считаться доступным")
	}
}

func TestValidateCatchesDuplicateUnitStatus(t *testing.T) {
	s := refdata.MustLoad()
	s.UnitStatuses = append(s.UnitStatuses, s.UnitStatuses[0])
	if err := s.Validate(); err == nil {
		t.Fatal("повтор статуса единицы должен быть ошибкой")
	}
}

func TestValidateCatchesBrokenReference(t *testing.T) {
	s := refdata.MustLoad()
	s.Clinics[0].DistrictID = 999
	if err := s.Validate(); err == nil {
		t.Fatal("ссылка на несуществующий район должна быть ошибкой")
	}
	s = refdata.MustLoad()
	s.BloodGroups[0].PopulationShare += 0.1
	if err := s.Validate(); err == nil {
		t.Fatal("сумма долей групп ≠ 1 должна быть ошибкой")
	}
}
