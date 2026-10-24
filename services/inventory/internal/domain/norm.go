package domain

import (
	"fmt"
	"math"
)

// Norm — демонстрационные формулы FR012: Kср=Kгод/12, N=max(Kгод/360, объём единицы).
type Norm struct {
	MonthlyML, NormML float64
	Minimum           bool
}

func CalculateNorm(annual, unit int64) (Norm, error) {
	if annual < 0 || annual > 1_000_000_000_000 || unit <= 0 {
		return Norm{}, fmt.Errorf("%w: неверный расход или объём единицы", ErrInvalidSource)
	}
	n := Norm{MonthlyML: math.Round(float64(annual)/12*1000) / 1000, NormML: math.Round(float64(annual)/360*1000) / 1000}
	if n.NormML < float64(unit) {
		n.NormML = float64(unit)
		n.Minimum = true
	}
	return n, nil
}
