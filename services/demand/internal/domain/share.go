package domain

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Share — доля от 0 до 1, округлённая до 4 знаков, как numeric(5,4) в ПР6. Хранится float64, но суммы и
// деления считаются в целых десятитысячных (TenThousandths): сравнение с допуском на границе
// 1 ± 0,0001 не должно зависеть от двоичной погрешности.
type Share float64

// shareScale — 4 знака после запятой.
const shareScale = 10000

// ShareSumToleranceUnits — допуск суммы долей района 1 ± 0,0001 в десятитысячных (задача 04, FR016, FR046).
// Демо-допуск (ADR-0010).
const ShareSumToleranceUnits = 1

// shareRe — только десятичная запись: без знака, экспоненты, NaN и Inf, которые принимает strconv.ParseFloat.
var shareRe = regexp.MustCompile(`^([0-9]{1,6})(?:[.,]([0-9]+))?$`)

// errShareFormat — понятное аналитику сообщение о формате доли.
var errShareFormat = errors.New("ожидается десятичная дробь, например 0,35")

// ParseShare разбирает десятичную долю с точкой или запятой и округляет до 4 знаков, половина — от нуля.
// Округление по десятичной записи, а не по float64: «0,12345» даёт ровно 0,1235. Диапазон не проверяет:
// допустимые границы у доли группы и доли клиники разные.
func ParseShare(s string) (Share, error) {
	m := shareRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, errShareFormat
	}
	whole, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, errShareFormat
	}
	frac := m[2]
	units := whole * shareScale
	digits := frac
	if len(digits) > 4 {
		digits = digits[:4]
	}
	digits += strings.Repeat("0", 4-len(digits))
	f, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, errShareFormat
	}
	units += f
	if len(frac) > 4 && frac[4] >= '5' {
		units++
	}
	return Share(float64(units) / shareScale), nil
}

// TenThousandths — доля в целых десятитысячных.
func (s Share) TenThousandths() int64 {
	return int64(math.Round(float64(s) * shareScale))
}

// ShareSumWithinTolerance — сумма долей в десятитысячных равна 1 ± 0,0001.
func ShareSumWithinTolerance(sumUnits int64) bool {
	d := sumUnits - shareScale
	return d >= -ShareSumToleranceUnits && d <= ShareSumToleranceUnits
}

// formatShareUnits печатает долю по-русски с запятой и 4 знаками: 12000 → «1,2000».
func formatShareUnits(units int64) string {
	sign := ""
	if units < 0 {
		sign, units = "-", -units
	}
	return fmt.Sprintf("%s%d,%04d", sign, units/shareScale, units%shareScale)
}
