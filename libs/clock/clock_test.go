package clock_test

import (
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
)

func TestMutableAdvance(t *testing.T) {
	start := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	c := clock.NewMutable(start)
	if got := c.Now(); !got.Equal(start) {
		t.Fatalf("Now = %v, ожидали %v", got, start)
	}
	c.Advance(15 * time.Minute)
	if got := c.Now(); !got.Equal(start.Add(15 * time.Minute)) {
		t.Fatalf("после Advance Now = %v", got)
	}
	c.Set(start)
	if got := c.Now(); !got.Equal(start) {
		t.Fatalf("после Set Now = %v", got)
	}
}

func TestSystemIsUTC(t *testing.T) {
	if loc := (clock.System{}).Now().Location(); loc != time.UTC {
		t.Fatalf("System.Now не в UTC: %v", loc)
	}
}
