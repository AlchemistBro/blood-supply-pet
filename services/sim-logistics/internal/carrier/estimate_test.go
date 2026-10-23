package carrier_test

import (
	"errors"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-logistics/internal/carrier"
)

var clinics = []carrier.Clinic{
	{ClinicID: 1, DistrictID: 1},
	{ClinicID: 2, DistrictID: 1},
	{ClinicID: 3, DistrictID: 2},
	{ClinicID: 4, DistrictID: 5},
}

func TestEstimatesAreDeterministicAndSymmetric(t *testing.T) {
	e := carrier.NewEstimator(clinics, clock.NewMutable(start))
	first, err := e.Estimate(1, 0)
	if err != nil {
		t.Fatalf("оценка: %v", err)
	}
	if len(first.Estimates) != 3 {
		t.Fatalf("оценок %d, ожидали 3 (все клиники, кроме источника)", len(first.Estimates))
	}
	if !first.EstimatedAt.Equal(start) {
		t.Fatalf("время оценки %v, ожидали время часов", first.EstimatedAt)
	}
	second, _ := e.Estimate(1, 0)
	for i := range first.Estimates {
		if first.Estimates[i] != second.Estimates[i] {
			t.Fatalf("оценка недетерминирована: %+v и %+v", first.Estimates[i], second.Estimates[i])
		}
	}
	there, _ := e.Estimate(1, 4)
	back, _ := e.Estimate(4, 1)
	if there.Estimates[0].Minutes != back.Estimates[0].Minutes {
		t.Fatalf("туда %d мин, обратно %d мин", there.Estimates[0].Minutes, back.Estimates[0].Minutes)
	}
	near, _ := e.Estimate(1, 2)
	if near.Estimates[0].Minutes >= there.Estimates[0].Minutes {
		t.Fatalf("внутри района %d мин, через четыре района %d мин", near.Estimates[0].Minutes, there.Estimates[0].Minutes)
	}
	for _, est := range first.Estimates {
		if est.Minutes <= 0 {
			t.Fatalf("неположительная оценка: %+v", est)
		}
	}
}

func TestEstimateRejectsUnknownAndSameClinic(t *testing.T) {
	e := carrier.NewEstimator(clinics, clock.NewMutable(start))
	if _, err := e.Estimate(99, 0); !errors.Is(err, carrier.ErrNotFound) {
		t.Fatalf("неизвестный источник: %v", err)
	}
	if _, err := e.Estimate(1, 99); !errors.Is(err, carrier.ErrNotFound) {
		t.Fatalf("неизвестный получатель: %v", err)
	}
	if _, err := e.Estimate(1, 1); !errors.Is(err, carrier.ErrInvalid) {
		t.Fatalf("источник равен получателю: %v", err)
	}
}
