package carrier

import (
	"fmt"
	"math"
	"regexp"
	"time"
)

// Progress вводится пультом демо: имитатор не отправляет груз автоматически.
type Progress struct {
	DispatchedAt             *time.Time `json:"dispatchedAt,omitempty"`
	DeliveredAt              *time.Time `json:"deliveredAt,omitempty"`
	CourierRef               string     `json:"courierRef,omitempty"`
	DepartureTemperature     *float64   `json:"temperatureAtDispatchC,omitempty"`
	ArrivalTemperature       *float64   `json:"temperatureAtArrivalC,omitempty"`
	DepartureContainerIntact *bool      `json:"departureContainerIntact,omitempty"`
	ArrivalContainerIntact   *bool      `json:"arrivalContainerIntact,omitempty"`
	TemperatureExcursion     *bool      `json:"temperatureExcursion,omitempty"`
}

var courierCode = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

func (c *Carrier) SetProgress(ref string, p Progress) error {
	if p.DispatchedAt == nil || !courierCode.MatchString(p.CourierRef) {
		return fmt.Errorf("%w: нужны dispatchedAt и courierRef", ErrInvalid)
	}
	for _, temperature := range []*float64{p.DepartureTemperature, p.ArrivalTemperature} {
		if temperature != nil && (math.IsNaN(*temperature) || math.IsInf(*temperature, 0) || *temperature < -80 || *temperature > 60) {
			return ErrInvalid
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock.Now()
	if p.DispatchedAt.After(now) || (p.DeliveredAt != nil && (p.DeliveredAt.Before(*p.DispatchedAt) || p.DeliveredAt.After(now))) {
		return ErrInvalid
	}
	for i := range c.shipments {
		s := &c.shipments[i]
		if s.ShipmentRef != ref {
			continue
		}
		if s.Cancelled || p.DispatchedAt.Before(s.CreatedAt) {
			return ErrInvalid
		}
		// Факт отправки и завершённая доставка неизменяемы: исправление не скрывает нарушение.
		if s.Progress.DeliveredAt != nil {
			return ErrInvalid
		}
		if s.Progress.DispatchedAt != nil {
			old := s.Progress
			if !old.DispatchedAt.Equal(*p.DispatchedAt) || old.CourierRef != p.CourierRef || !equalFloat(old.DepartureTemperature, p.DepartureTemperature) || !equalBool(old.DepartureContainerIntact, p.DepartureContainerIntact) {
				return ErrInvalid
			}
		}
		s.Progress = p
		return nil
	}
	return ErrNotFound
}
func equalFloat(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
func equalBool(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
