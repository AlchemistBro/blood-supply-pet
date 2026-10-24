package domain

import (
	"errors"
	"math"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
)

var ErrInvalidTransport = errors.New("неверные данные перевозки")

type TransportConditions struct {
	DepartedAt, ArrivedAt                            time.Time
	DepartureTemperature, ArrivalTemperature         *float64
	DepartureContainerIntact, ArrivalContainerIntact bool
	TemperatureExcursion                             bool
}

// EvaluateTransport — демо-проверка ADR-0022/0033. Отсутствие обязательной регистрации не считается нормой.
func EvaluateTransport(kind string, c TransportConditions) ([]string, error) {
	if c.DepartedAt.IsZero() || c.ArrivedAt.Before(c.DepartedAt) {
		return nil, ErrInvalidTransport
	}
	violations := []string{}
	if !c.DepartureContainerIntact || !c.ArrivalContainerIntact {
		violations = append(violations, transfer.ViolationContainer)
	}
	duration := c.ArrivedAt.Sub(c.DepartedAt)
	if duration > 24*time.Hour {
		violations = append(violations, transfer.ViolationDuration)
	}
	if duration > 30*time.Minute && (c.DepartureTemperature == nil || c.ArrivalTemperature == nil) {
		violations = append(violations, transfer.ViolationTemperatureNotRecorded)
	}
	badTemperature := c.TemperatureExcursion
	for _, value := range []*float64{c.DepartureTemperature, c.ArrivalTemperature} {
		if value == nil {
			continue
		}
		if math.IsNaN(*value) || math.IsInf(*value, 0) || *value < -100 || *value > 100 {
			return nil, ErrInvalidTransport
		}
		switch kind {
		case "RBC":
			badTemperature = badTemperature || *value < 2 || *value > 10
		case "PLT":
			badTemperature = badTemperature || *value < 20 || *value > 24
		case "FFP", "CRYO":
			badTemperature = badTemperature || *value > -18
		default:
			return nil, ErrInvalidTransport
		}
	}
	if kind != "RBC" && kind != "PLT" && kind != "FFP" && kind != "CRYO" {
		return nil, ErrInvalidTransport
	}
	if badTemperature {
		violations = append(violations, transfer.ViolationTemperature)
	}
	return violations, nil
}
