package carrier_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-logistics/internal/carrier"
)

var start = time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)

func order(clientRef string) carrier.Order {
	return carrier.Order{
		ClientRef:         clientRef,
		SourceClinicID:    1,
		TargetClinicID:    2,
		ComponentKind:     "RBC",
		Containers:        1,
		TemperatureRegime: carrier.Cold2to6,
	}
}

func TestSubmitDoesNotDeduplicate(t *testing.T) {
	c := carrier.New(clock.NewMutable(start))
	first, err := c.Submit(context.Background(), order("o07-1"))
	if err != nil {
		t.Fatalf("первый заказ: %v", err)
	}
	second, err := c.Submit(context.Background(), order("o07-1"))
	if err != nil {
		t.Fatalf("второй заказ: %v", err)
	}
	if first.Shipment.ShipmentRef == second.Shipment.ShipmentRef {
		t.Fatalf("номера заказов совпали: %s", first.Shipment.ShipmentRef)
	}
	if first.Shipment.ShipmentRef != "C03-1" || second.Shipment.ShipmentRef != "C03-2" {
		t.Fatalf("номера заказов: %s, %s; ожидали C03-1, C03-2", first.Shipment.ShipmentRef, second.Shipment.ShipmentRef)
	}
	active, err := c.FindActive("o07-1")
	if err != nil {
		t.Fatalf("сверка: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("активных заказов %d, ожидали 2: имитатор нарочно не дедуплицирует", len(active))
	}
	if !first.Shipment.CreatedAt.Equal(start) {
		t.Fatalf("время создания %v, ожидали время часов %v", first.Shipment.CreatedAt, start)
	}
}

func TestLostResponseStillCreatesShipment(t *testing.T) {
	c := carrier.New(clock.NewMutable(start))
	c.UpdateFaults(func(f *carrier.Faults) { f.LoseNextSubmitResponse = true })

	res, err := c.Submit(context.Background(), order("o07-2"))
	if err != nil {
		t.Fatalf("заказ: %v", err)
	}
	if !res.ResponseLost {
		t.Fatal("ответ должен быть помечен потерянным")
	}
	active, err := c.FindActive("o07-2")
	if err != nil || len(active) != 1 {
		t.Fatalf("сверка: %v, заказов %d, ожидали 1", err, len(active))
	}
	if c.Faults().LoseNextSubmitResponse {
		t.Fatal("потеря ответа одноразовая")
	}
	next, err := c.Submit(context.Background(), order("o07-3"))
	if err != nil || next.ResponseLost {
		t.Fatalf("следующий заказ: %v, потерян=%v", err, next.ResponseLost)
	}
}

func TestFailNextCallsCountsSubmitFindAndCancel(t *testing.T) {
	c := carrier.New(clock.NewMutable(start))
	c.UpdateFaults(func(f *carrier.Faults) { f.FailNextCalls = 3 })

	if _, err := c.Submit(context.Background(), order("o07-4")); !errors.Is(err, carrier.ErrUnavailable) {
		t.Fatalf("submit: %v, ожидали ErrUnavailable", err)
	}
	if _, err := c.FindActive("o07-4"); !errors.Is(err, carrier.ErrUnavailable) {
		t.Fatalf("find: %v, ожидали ErrUnavailable", err)
	}
	if _, err := c.CancelByClientRef("o07-4"); !errors.Is(err, carrier.ErrUnavailable) {
		t.Fatalf("cancel: %v, ожидали ErrUnavailable", err)
	}
	if len(c.All("")) != 0 {
		t.Fatal("недоступный перевозчик не создаёт заказ")
	}
	if _, err := c.Submit(context.Background(), order("o07-4")); err != nil {
		t.Fatalf("после трёх сбоев заказ должен пройти: %v", err)
	}
}

func TestRefuseNextCreatesNothing(t *testing.T) {
	c := carrier.New(clock.NewMutable(start))
	c.UpdateFaults(func(f *carrier.Faults) {
		f.RefuseNext = "нет свободной машины с термоконтейнером"
	})

	_, err := c.Submit(context.Background(), order("o07-5"))
	var refused *carrier.RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("ожидали отказ перевозчика, получили %v", err)
	}
	if refused.Reason != "нет свободной машины с термоконтейнером" {
		t.Fatalf("причина отказа: %q", refused.Reason)
	}
	if len(c.All("")) != 0 {
		t.Fatal("отказ не создаёт заказ")
	}
	if _, err := c.Submit(context.Background(), order("o07-5")); err != nil {
		t.Fatalf("отказ одноразовый: %v", err)
	}
}

func TestCancelHidesFromActiveButKeepsInAll(t *testing.T) {
	clk := clock.NewMutable(start)
	c := carrier.New(clk)
	first, _ := c.Submit(context.Background(), order("o07-6"))
	_, _ = c.Submit(context.Background(), order("o07-6"))
	other, _ := c.Submit(context.Background(), order("o07-7"))

	clk.Advance(time.Minute)
	cancelled, err := c.CancelByShipmentRef(first.Shipment.ShipmentRef)
	if err != nil {
		t.Fatalf("отмена по номеру: %v", err)
	}
	if !cancelled.Cancelled || cancelled.CancelledAt == nil || !cancelled.CancelledAt.Equal(start.Add(time.Minute)) {
		t.Fatalf("отменённый заказ: %+v", cancelled)
	}
	active, _ := c.FindActive("o07-6")
	if len(active) != 1 {
		t.Fatalf("после отмены одного из двух активных %d, ожидали 1", len(active))
	}

	byClient, err := c.CancelByClientRef("o07-6")
	if err != nil {
		t.Fatalf("отмена по clientRef: %v", err)
	}
	if len(byClient) != 2 {
		t.Fatalf("отмена по clientRef вернула %d заказов, ожидали 2", len(byClient))
	}
	if active, _ := c.FindActive("o07-6"); len(active) != 0 {
		t.Fatalf("отменённые заказы видны в сверке: %d", len(active))
	}
	if all := c.All("o07-6"); len(all) != 2 || !all[0].Cancelled || !all[1].Cancelled {
		t.Fatalf("все заказы clientRef: %+v", all)
	}
	if active, _ := c.FindActive("o07-7"); len(active) != 1 || active[0].ShipmentRef != other.Shipment.ShipmentRef {
		t.Fatalf("отмена задела чужой заказ: %+v", active)
	}
	if len(c.All("")) != 3 {
		t.Fatalf("всего заказов %d, ожидали 3", len(c.All("")))
	}

	// Повторная отмена ничего не ломает и не сдвигает время отмены.
	clk.Advance(time.Minute)
	again, err := c.CancelByShipmentRef(first.Shipment.ShipmentRef)
	if err != nil || !again.CancelledAt.Equal(start.Add(time.Minute)) {
		t.Fatalf("повторная отмена: %v, %+v", err, again)
	}
	if _, err := c.CancelByShipmentRef("C03-999"); !errors.Is(err, carrier.ErrNotFound) {
		t.Fatalf("отмена неизвестного заказа: %v, ожидали ErrNotFound", err)
	}
}

func TestSlowSubmitHonoursContext(t *testing.T) {
	c := carrier.New(clock.NewMutable(start))
	c.UpdateFaults(func(f *carrier.Faults) { f.Slow = time.Hour })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Submit(ctx, order("o07-8")); !errors.Is(err, context.Canceled) {
		t.Fatalf("ожидали отмену контекста, получили %v", err)
	}
	if len(c.All("")) != 0 {
		t.Fatal("заказ не создаётся, если клиент ушёл во время задержки")
	}
}

// Задержка отправки идёт вне блокировки: пока первый экземпляр шлюза ждёт ответа, сверка второго отвечает сразу.
// Иначе окно для дубля закрылось бы, и тест двух экземпляров шлюза проверял бы имитатор, а не аренду.
// Пауза подменена: она сообщает «вошла» и ждёт отмены контекста, реального ожидания нет. Если бы пауза держала
// блокировку, FindActive не ответил бы, и тест упал бы по своему сроку, а не завис.
func TestFindActiveAnswersDuringSlowSubmit(t *testing.T) {
	const deadline = 10 * time.Second
	c := carrier.New(clock.NewMutable(start))
	if _, err := c.Submit(context.Background(), order("o07-9")); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	c.SetSleep(func(ctx context.Context, _ time.Duration) error {
		close(entered)
		<-ctx.Done()
		return fmt.Errorf("задержка прервана: %w", ctx.Err())
	})
	c.UpdateFaults(func(f *carrier.Faults) { f.Slow = time.Hour })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.Submit(ctx, order("o07-9"))
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(deadline):
		t.Fatal("отправка не дошла до паузы")
	}

	found := make(chan int, 1)
	go func() {
		active, err := c.FindActive("o07-9")
		if err != nil {
			found <- -1
			return
		}
		found <- len(active)
	}()
	select {
	case n := <-found:
		if n != 1 {
			t.Fatalf("сверка во время медленной отправки нашла %d заказов, ожидали 1", n)
		}
	case <-time.After(deadline):
		t.Fatal("сверка ждёт медленную отправку: пауза держит блокировку")
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("медленная отправка: ожидали отмену контекста, получили %v", err)
	}
	if got := len(c.All("o07-9")); got != 1 {
		t.Fatalf("прерванная отправка создала заказ: всего %d", got)
	}
}

func TestOrderValidation(t *testing.T) {
	cases := map[string]func(o *carrier.Order){
		"пустой clientRef":          func(o *carrier.Order) { o.ClientRef = "" },
		"нет источника":             func(o *carrier.Order) { o.SourceClinicID = 0 },
		"нет получателя":            func(o *carrier.Order) { o.TargetClinicID = 0 },
		"источник равен получателю": func(o *carrier.Order) { o.TargetClinicID = o.SourceClinicID },
		"нет вида компонента":       func(o *carrier.Order) { o.ComponentKind = "" },
		"ноль контейнеров":          func(o *carrier.Order) { o.Containers = 0 },
		"слишком много контейнеров": func(o *carrier.Order) { o.Containers = carrier.MaxContainers + 1 },
		"неизвестный режим":         func(o *carrier.Order) { o.TemperatureRegime = "WARM" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			o := order("o07-9")
			mutate(&o)
			if err := o.Validate(); !errors.Is(err, carrier.ErrInvalid) {
				t.Fatalf("ожидали ErrInvalid, получили %v", err)
			}
		})
	}
	if err := order("o07-9").Validate(); err != nil {
		t.Fatalf("корректный заказ отклонён: %v", err)
	}
}

func TestResetClearsShipmentsAndFaultsButKeepsNumbering(t *testing.T) {
	c := carrier.New(clock.NewMutable(start))
	_, _ = c.Submit(context.Background(), order("o07-10"))
	c.UpdateFaults(func(f *carrier.Faults) {
		f.FailNextCalls = 5
		f.RefuseNext = "нет машины"
		f.LoseNextSubmitResponse = true
		f.Slow = time.Second
	})
	c.Reset()
	if (c.Faults() != carrier.Faults{}) {
		t.Fatalf("сбои после сброса: %+v", c.Faults())
	}
	if len(c.All("")) != 0 {
		t.Fatal("заказы после сброса остались")
	}
	res, err := c.Submit(context.Background(), order("o07-10"))
	if err != nil {
		t.Fatalf("заказ после сброса: %v", err)
	}
	if res.Shipment.ShipmentRef != "C03-2" {
		t.Fatalf("номер после сброса %s: нумерация не должна начинаться заново, иначе старые ссылки шлюза совпадут с новыми", res.Shipment.ShipmentRef)
	}
}
