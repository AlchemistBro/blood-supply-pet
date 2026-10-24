package domain

import "time"

// RetryPolicy — повторы отправки письма. Сбой почты не меняет задание (FR044): письмо ждёт следующей попытки.
type RetryPolicy struct {
	// Base — пауза после первой неудачи; дальше удваивается.
	Base time.Duration
	// Max — верхняя граница паузы.
	Max time.Duration
	// MaxAttempts — после стольких неудачных попыток запись FAILED.
	MaxAttempts int
}

// DefaultRetryPolicy — 30 с, 1, 2, 4, 8 мин, затем каждые 15 мин; 12 попыток — около двух часов (демо).
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{Base: 30 * time.Second, Max: 15 * time.Minute, MaxAttempts: 12}
}

// Delay — пауза после attempts неудачных попыток (attempts ≥ 1).
func (p RetryPolicy) Delay(attempts int) time.Duration {
	d := p.Base
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= p.Max {
			return p.Max
		}
	}
	return min(d, p.Max)
}

// AfterFailure — исход неудачной попытки номер attempts: повтор позже или окончательный отказ.
// permanent — сервер отклонил письмо (код 5xx), повтор бессмыслен.
func (p RetryPolicy) AfterFailure(attempts int, permanent bool, now time.Time) (Status, time.Time) {
	if permanent || attempts >= p.MaxAttempts {
		return StatusFailed, now
	}
	return StatusPending, now.Add(p.Delay(attempts))
}
