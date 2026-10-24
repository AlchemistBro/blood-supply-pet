package domain_test

import (
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
)

func unitExpiring(id string, expires time.Time) domain.Unit {
	u := domain.Unit{UnitID: uuid.MustParse(id)}
	u.ExpiresAt = expires
	return u
}

// FEFO (FR015): первой выдаётся единица с ближайшей годностью; при равной годности порядок стабилен по unit_id.
func TestSortFEFO(t *testing.T) {
	a := unitExpiring("01920000-0000-7000-8000-00000000000a", t0.Add(72*time.Hour))
	b := unitExpiring("01920000-0000-7000-8000-00000000000b", t0.Add(24*time.Hour))
	c := unitExpiring("01920000-0000-7000-8000-00000000000c", t0.Add(24*time.Hour))
	d := unitExpiring("01920000-0000-7000-8000-00000000000d", t0.Add(48*time.Hour))
	units := []domain.Unit{a, d, c, b}
	domain.SortFEFO(units)
	want := []uuid.UUID{b.UnitID, c.UnitID, d.UnitID, a.UnitID}
	for i, u := range units {
		if u.UnitID != want[i] {
			t.Fatalf("позиция %d: %s, ожидали %s", i, u.UnitID, want[i])
		}
	}
}

// Истёкшая единица недоступна даже в статусе «в наличии» (FR007); граница — сам момент окончания годности.
func TestExpiredUnitIsNotUsable(t *testing.T) {
	at := t0
	cases := []struct {
		name            string
		statusAvailable bool
		expires         time.Time
		want            bool
	}{
		{"в наличии, годна", true, at.Add(time.Hour), true},
		{"в наличии, истекла", true, at.Add(-time.Second), false},
		{"в наличии, истекает ровно сейчас", true, at, false},
		{"недоступный статус, годна", false, at.Add(time.Hour), false},
	}
	for _, c := range cases {
		if got := domain.Usable(c.statusAvailable, c.expires, at); got != c.want {
			t.Errorf("%s: %v, ожидали %v", c.name, got, c.want)
		}
	}
}

// FR011: объём в литрах — сумма миллилитров, делённая на 1000, с явным округлением до миллилитра.
func TestLitres(t *testing.T) {
	cases := map[int64]float64{0: 0, 300: 0.3, 1250: 1.25, 999_999: 999.999}
	for ml, want := range cases {
		if got := domain.Litres(ml); math.Abs(got-want) > 1e-12 {
			t.Errorf("Litres(%d) = %v, ожидали %v", ml, got, want)
		}
	}
}

// NFR015: копия устарела, если успешного обновления не было или оно старше порога.
func TestStale(t *testing.T) {
	limit := 10 * time.Minute
	recent := t0.Add(-9 * time.Minute)
	old := t0.Add(-11 * time.Minute)
	if domain.Stale(nil, t0, limit) != true {
		t.Error("без обновлений копия должна считаться устаревшей")
	}
	if domain.Stale(&recent, t0, limit) {
		t.Error("обновление 9 минут назад при пороге 10 минут — не устарело")
	}
	if !domain.Stale(&old, t0, limit) {
		t.Error("обновление 11 минут назад при пороге 10 минут — устарело")
	}
}
