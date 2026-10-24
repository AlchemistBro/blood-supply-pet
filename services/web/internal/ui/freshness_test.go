package ui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
)

func TestNewFreshnessWithinThreshold(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	f := NewFreshness(now.Add(-15*time.Minute), now, 15*time.Minute)
	if !f.Known || f.Stale {
		t.Fatalf("ровно на пороге данные ещё свежие: %+v", f)
	}
	if f.Age != 15*time.Minute {
		t.Fatalf("age = %v", f.Age)
	}
	if f.ShowDate {
		t.Fatal("в тот же день по Москве дата не нужна")
	}
}

func TestNewFreshnessStaleAfterThreshold(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	f := NewFreshness(now.Add(-15*time.Minute-time.Second), now, 15*time.Minute)
	if !f.Stale {
		t.Fatalf("после порога нужно предупреждение NFR015: %+v", f)
	}
}

func TestNewFreshnessShowsDateForPreviousMoscowDay(t *testing.T) {
	// 20:59 UTC 22.10 — это 23:59 22.10 по Москве; «сейчас» 21:01 UTC — уже 00:01 23.10 по Москве.
	updated := time.Date(2026, 10, 22, 20, 59, 0, 0, time.UTC)
	now := time.Date(2026, 10, 22, 21, 1, 0, 0, time.UTC)
	f := NewFreshness(updated, now, time.Hour)
	if !f.ShowDate {
		t.Fatal("обновление было в прошлые сутки по Москве: нужна дата")
	}
	if f.Stale {
		t.Fatal("две минуты — не устаревание")
	}
}

func TestNewFreshnessZeroIsUnknownAndStale(t *testing.T) {
	f := NewFreshness(time.Time{}, time.Now(), time.Hour)
	if f.Known || !f.Stale {
		t.Fatalf("ни одной синхронизации: неизвестно и с предупреждением, got %+v", f)
	}
}

func TestNewFreshnessFutureClampsToZero(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	f := NewFreshness(now.Add(time.Minute), now, time.Hour)
	if f.Age != 0 || f.Stale {
		t.Fatalf("время из будущего (рассинхрон часов) не делает данные устаревшими: %+v", f)
	}
}

type fakeSource struct {
	at  time.Time
	err error
}

func (f fakeSource) LastStockSync(context.Context) (time.Time, error) { return f.at, f.err }

func TestLoadFreshnessUsesClock(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	clk := clock.NewMutable(now)
	f := LoadFreshness(context.Background(), fakeSource{at: now.Add(-2 * time.Hour)}, clk, time.Hour)
	if !f.Known || !f.Stale || f.Age != 2*time.Hour {
		t.Fatalf("got %+v", f)
	}
}

func TestLoadFreshnessErrorIsUnknown(t *testing.T) {
	clk := clock.NewMutable(time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC))
	f := LoadFreshness(context.Background(), fakeSource{err: errors.New("inventory недоступен")}, clk, time.Hour)
	if f.Known || !f.Stale {
		t.Fatalf("сервис запаса не ответил: неизвестно и с предупреждением, got %+v", f)
	}
}
