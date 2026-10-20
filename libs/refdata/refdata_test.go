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
