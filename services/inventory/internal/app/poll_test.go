package app_test

import (
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/app"
)

// Пока АИСТ-М недоступна, пауза между опросами удваивается до предела; после успеха — обычный интервал.
func TestNextPollDelayBacksOffOnFailures(t *testing.T) {
	interval, maxDelay := 5*time.Second, time.Minute
	d := interval
	want := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, time.Minute, time.Minute}
	for i, w := range want {
		d = app.NextPollDelay(d, interval, maxDelay, true)
		if d != w {
			t.Fatalf("отказ %d: пауза %v, ожидали %v", i+1, d, w)
		}
	}
	if d = app.NextPollDelay(d, interval, maxDelay, false); d != interval {
		t.Fatalf("после успеха пауза %v, ожидали %v", d, interval)
	}
}
