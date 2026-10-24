package domain_test

import (
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/domain"
)

func TestRetryDelayDoublesUpToMax(t *testing.T) {
	p := domain.RetryPolicy{Base: 30 * time.Second, Max: 5 * time.Minute, MaxAttempts: 10}
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	for i, w := range want {
		if got := p.Delay(i + 1); got != w {
			t.Errorf("попытка %d: пауза %s, ожидалась %s", i+1, got, w)
		}
	}
	if got := p.Delay(200); got != 5*time.Minute {
		t.Errorf("большое число попыток: пауза %s, ожидалась %s (без переполнения)", got, 5*time.Minute)
	}
}

// Недоступный SMTP: запись остаётся PENDING и ждёт следующей попытки; после последней — FAILED.
func TestAfterFailureKeepsPendingUntilAttemptsExhausted(t *testing.T) {
	p := domain.RetryPolicy{Base: 30 * time.Second, Max: 5 * time.Minute, MaxAttempts: 3}
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)

	status, next := p.AfterFailure(1, false, now)
	if status != domain.StatusPending || !next.Equal(now.Add(30*time.Second)) {
		t.Fatalf("первая неудача: %s %s, ожидалось PENDING через 30 с", status, next)
	}
	status, next = p.AfterFailure(2, false, now)
	if status != domain.StatusPending || !next.Equal(now.Add(time.Minute)) {
		t.Fatalf("вторая неудача: %s %s, ожидалось PENDING через минуту", status, next)
	}
	if status, _ = p.AfterFailure(3, false, now); status != domain.StatusFailed {
		t.Fatalf("попытки исчерпаны: %s, ожидалось FAILED", status)
	}
}

// Постоянный отказ сервера (5xx) повторять бессмысленно.
func TestAfterPermanentFailureFailsAtOnce(t *testing.T) {
	p := domain.DefaultRetryPolicy()
	if status, _ := p.AfterFailure(1, true, time.Now()); status != domain.StatusFailed {
		t.Fatalf("постоянный отказ: %s, ожидалось FAILED", status)
	}
}
