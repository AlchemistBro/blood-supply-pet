package app_test

import (
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
)

func unitIn(status string, expires time.Time) domain.Unit {
	var u domain.Unit
	u.Status = status
	u.ExpiresAt = expires
	return u
}

// Единица на карантине не выдаётся и не передаётся (ПП РФ № 641, п. 62 а): в запас не входит, даже годная.
func TestQuarantinedUnitIsNotAvailable(t *testing.T) {
	ref := refdata.MustLoad()
	at := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	if app.Usable(ref, unitIn(domain.StatusQuarantine, at.Add(30*24*time.Hour)), at) {
		t.Fatal("единица на карантине вошла в фактический запас")
	}
	if !app.Usable(ref, unitIn(domain.StatusInStock, at.Add(30*24*time.Hour)), at) {
		t.Fatal("годная единица в наличии не вошла в фактический запас")
	}
}

// Доступность берётся из справочника ref_unit_status: выбывшие и неизвестные статусы недоступны,
// истёкшая единица в наличии тоже недоступна (FR007).
func TestUsableFollowsStatusDictionaryAndExpiry(t *testing.T) {
	ref := refdata.MustLoad()
	at := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	fresh := at.Add(time.Hour)
	for _, s := range []string{domain.StatusIssued, domain.StatusDiscarded, domain.StatusTransferred, "UNKNOWN"} {
		if app.Usable(ref, unitIn(s, fresh), at) {
			t.Errorf("статус %s не должен быть доступен", s)
		}
	}
	if app.Usable(ref, unitIn(domain.StatusInStock, at.Add(-time.Minute)), at) {
		t.Error("истёкшая единица вошла в фактический запас")
	}
}
