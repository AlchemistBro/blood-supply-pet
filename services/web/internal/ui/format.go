package ui

import (
	"strconv"
	"strings"
	"time"
)

// Moscow — часовой пояс интерфейса. Москва живёт в UTC+3 без перехода на летнее время с 26.10.2014,
// поэтому фиксированная зона: не нужна база часовых поясов в образе distroless, вывод одинаков на любой машине.
var Moscow = time.FixedZone("MSK", 3*60*60)

const (
	dash = "—"
	nbsp = " "
)

// FormatDateTime — «23.10.2026 14:32» по Москве; нулевое время — прочерк.
func FormatDateTime(t time.Time) string {
	if t.IsZero() {
		return dash
	}
	return t.In(Moscow).Format("02.01.2006 15:04")
}

// FormatDate — «23.10.2026» по Москве; нулевое время — прочерк.
func FormatDate(t time.Time) string {
	if t.IsZero() {
		return dash
	}
	return t.In(Moscow).Format("02.01.2006")
}

// FormatTime — «14:32» по Москве; нулевое время — прочерк.
func FormatTime(t time.Time) string {
	if t.IsZero() {
		return dash
	}
	return t.In(Moscow).Format("15:04")
}

// FormatML — объём в миллилитрах с разрядами: «1 200 мл». Пробелы неразрывные, чтобы число не отрывалось от единицы.
func FormatML(v int64) string {
	digits := strconv.FormatInt(v, 10)
	sign := ""
	if v < 0 {
		sign, digits = "-", digits[1:]
	}
	var b strings.Builder
	b.WriteString(sign)
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteString(nbsp)
		}
		b.WriteRune(r)
	}
	b.WriteString(nbsp + "мл")
	return b.String()
}

// FormatDuration — длительность для людей: «35 мин», «1 ч 12 мин», «1 сут 2 ч». Округление вниз до минут;
// отрицательная и меньше минуты — «меньше минуты». Сокращения без склонений (формальный стиль, style-guide.md).
func FormatDuration(d time.Duration) string {
	if d < time.Minute {
		return "меньше минуты"
	}
	minutes := int64(d / time.Minute)
	days := minutes / (24 * 60)
	hours := minutes % (24 * 60) / 60
	mins := minutes % 60

	var parts []string
	switch {
	case days > 0:
		parts = append(parts, strconv.FormatInt(days, 10)+" сут")
		if hours > 0 {
			parts = append(parts, strconv.FormatInt(hours, 10)+" ч")
		}
	case hours > 0:
		parts = append(parts, strconv.FormatInt(hours, 10)+" ч")
		if mins > 0 {
			parts = append(parts, strconv.FormatInt(mins, 10)+" мин")
		}
	default:
		parts = append(parts, strconv.FormatInt(mins, 10)+" мин")
	}
	return strings.Join(parts, " ")
}
