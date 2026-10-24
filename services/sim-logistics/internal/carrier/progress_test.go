package carrier_test

import (
	"context"
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-logistics/internal/carrier"
)

func TestProgressCannotRewriteDepartureOrCompletedDelivery(t *testing.T) {
	clk := clock.NewMutable(start)
	c := carrier.New(clk)
	result, err := c.Submit(context.Background(), order("o07-progress"))
	if err != nil {
		t.Fatal(err)
	}
	departed := start
	temp := 4.0
	intact := true
	p := carrier.Progress{DispatchedAt: &departed, CourierRef: "courier-1", DepartureTemperature: &temp, DepartureContainerIntact: &intact}
	if err := c.SetProgress(result.Shipment.ShipmentRef, p); err != nil {
		t.Fatal(err)
	}
	bad := 11.0
	p.DepartureTemperature = &bad
	if err := c.SetProgress(result.Shipment.ShipmentRef, p); err == nil {
		t.Fatal("температура отправки переписана")
	}
	p.DepartureTemperature = &temp
	clk.Advance(time.Hour)
	arrival := clk.Now()
	p.DeliveredAt = &arrival
	p.ArrivalTemperature = &bad
	p.ArrivalContainerIntact = &intact
	if err := c.SetProgress(result.Shipment.ShipmentRef, p); err != nil {
		t.Fatal(err)
	}
	p.ArrivalTemperature = &temp
	if err := c.SetProgress(result.Shipment.ShipmentRef, p); err == nil {
		t.Fatal("нарушение температуры скрыто исправлением завершённой перевозки")
	}
	rows := c.All("")
	if len(rows) != 1 || rows[0].Progress.ArrivalTemperature == nil || *rows[0].Progress.ArrivalTemperature != bad {
		t.Fatal("нарушение потеряно")
	}
}
