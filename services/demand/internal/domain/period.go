package domain

import (
	"errors"
	"time"
)

// dateLayout — формат даты в файлах аналитика.
const dateLayout = "2006-01-02"

// Period — период актуальности или расчёта в целых днях; обе границы включаются (как date в ПР6).
type Period struct {
	Start time.Time
	End   time.Time
}

// NewPeriod приводит границы к дате в UTC и проверяет, что конец не раньше начала.
func NewPeriod(start, end time.Time) (Period, error) {
	p := Period{Start: toDate(start), End: toDate(end)}
	if p.End.Before(p.Start) {
		return Period{}, errors.New("конец периода раньше начала")
	}
	return p, nil
}

// Days — число дней периода с обеими границами.
func (p Period) Days() int64 {
	return int64(p.End.Sub(p.Start).Hours()/24) + 1
}

// Overlaps — у периодов есть общий день.
func (p Period) Overlaps(q Period) bool {
	return !p.End.Before(q.Start) && !q.End.Before(p.Start)
}

// IsZero — период не задан.
func (p Period) IsZero() bool {
	return p.Start.IsZero() && p.End.IsZero()
}

func toDate(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func parseDate(s string) (time.Time, error) {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return time.Time{}, errors.New("дата в формате ГГГГ-ММ-ДД, например 2026-01-31")
	}
	return t, nil
}
