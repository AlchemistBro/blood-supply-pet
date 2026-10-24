package domain_test

import (
	"slices"
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

func TestTransportMissingTemperatureAndBadContainerCannotPassAsNormal(t *testing.T) {
	start := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	good, bad := 4.0, 11.0
	for _, tc := range []struct {
		duration           time.Duration
		departure, arrival *float64
		intact             bool
		want               string
	}{
		{30 * time.Minute, nil, nil, true, ""}, {31 * time.Minute, nil, &good, true, transfer.ViolationTemperatureNotRecorded},
		{time.Hour, &good, &bad, true, transfer.ViolationTemperature}, {time.Hour, &good, &good, false, transfer.ViolationContainer},
		{25 * time.Hour, &good, &good, true, transfer.ViolationDuration},
	} {
		violations, err := domain.EvaluateTransport("RBC", domain.TransportConditions{DepartedAt: start, ArrivedAt: start.Add(tc.duration), DepartureTemperature: tc.departure, ArrivalTemperature: tc.arrival, DepartureContainerIntact: true, ArrivalContainerIntact: tc.intact})
		if err != nil || (tc.want == "" && len(violations) > 0) || (tc.want != "" && !slices.Contains(violations, tc.want)) {
			t.Fatalf("проверка перевозки: %v %v", violations, err)
		}
	}
}
