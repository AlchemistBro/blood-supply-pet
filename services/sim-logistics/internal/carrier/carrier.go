// Package carrier — состояние имитатора перевозчика C03 в памяти: заказы перевозки, внедрение сбоев и оценки
// времени доставки. Перенос SimulatedC03 из прототипа.
//
// Имитатор нарочно не дедуплицирует заказы: два одинаковых заказа дают две машины. «Ровно один заказ»
// обязан обеспечить logistics-gateway (сверка по clientRef, аренда строки), а не удачный перевозчик.
// Номеров единиц крови заказ не содержит: замена единицы не трогает перевозку (ADR-0011).
package carrier

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
)

// TemperatureRegime — температурный режим перевозки термоконтейнера.
type TemperatureRegime string

// Режимы перевозки (демо: набор синтетический, ADR-0010).
const (
	Cold2to6      TemperatureRegime = "COLD_2_6"
	FrozenMinus25 TemperatureRegime = "FROZEN_MINUS_25"
	Room20to24    TemperatureRegime = "ROOM_20_24"
)

// MaxContainers — верхняя граница числа контейнеров в одном заказе (демо).
const MaxContainers = 50

// shipmentPrefix — префикс номера заказа у перевозчика: C03-1, C03-2, ...
const shipmentPrefix = "C03-"

// Valid сообщает, известен ли режим.
func (r TemperatureRegime) Valid() bool {
	switch r {
	case Cold2to6, FrozenMinus25, Room20to24:
		return true
	default:
		return false
	}
}

var (
	// ErrUnavailable — перевозчик не отвечает; для отправки исход неизвестен только при потере ответа.
	ErrUnavailable = errors.New("C03 не отвечает")
	// ErrNotFound — заказ или клиника не найдены.
	ErrNotFound = errors.New("не найдено")
	// ErrInvalid — запрос не прошёл проверку.
	ErrInvalid = errors.New("некорректный запрос")
)

// RefusedError — отказ перевозчика принять заказ (например, нет машины). Заказ не создан.
type RefusedError struct {
	Reason string
}

func (e *RefusedError) Error() string { return "перевозчик отказал: " + e.Reason }

// Order — заказ перевозки от шлюза.
type Order struct {
	ClientRef         string
	SourceClinicID    int64
	TargetClinicID    int64
	ComponentKind     string
	Containers        int
	TemperatureRegime TemperatureRegime
}

// Validate проверяет заказ до обращения к перевозчику.
func (o Order) Validate() error {
	switch {
	case o.ClientRef == "":
		return fmt.Errorf("%w: не указан clientRef", ErrInvalid)
	case o.SourceClinicID <= 0 || o.TargetClinicID <= 0:
		return fmt.Errorf("%w: не указаны клиника-источник и клиника-получатель", ErrInvalid)
	case o.SourceClinicID == o.TargetClinicID:
		return fmt.Errorf("%w: источник и получатель совпадают", ErrInvalid)
	case o.ComponentKind == "":
		return fmt.Errorf("%w: не указан вид компонента", ErrInvalid)
	case o.Containers < 1 || o.Containers > MaxContainers:
		return fmt.Errorf("%w: число контейнеров вне диапазона 1..%d", ErrInvalid, MaxContainers)
	case !o.TemperatureRegime.Valid():
		return fmt.Errorf("%w: неизвестный температурный режим %q", ErrInvalid, o.TemperatureRegime)
	default:
		return nil
	}
}

// Shipment — заказ у перевозчика.
type Shipment struct {
	ShipmentRef string
	Order
	Cancelled   bool
	CreatedAt   time.Time
	CancelledAt *time.Time
}

// SubmitResult — итог отправки. ResponseLost: заказ создан, но ответ клиенту доставлять нельзя.
type SubmitResult struct {
	Shipment     Shipment
	ResponseLost bool
}

// Faults — внедрённые сбои. Нулевое значение: перевозчик здоров.
type Faults struct {
	// FailNextCalls — столько следующих вызовов отправки, сверки или отмены отвечают «недоступен».
	FailNextCalls int
	// LoseNextSubmitResponse — следующая отправка создаёт заказ, но ответ теряется.
	LoseNextSubmitResponse bool
	// RefuseNext — непустая причина: следующая отправка получает отказ перевозчика.
	RefuseNext string
	// Slow — задержка перед отправкой: окно, в котором второй экземпляр шлюза мог бы отправить дубль.
	Slow time.Duration
}

// Carrier — имитатор C03. Безопасен для параллельных вызовов.
type Carrier struct {
	clock clock.Clock
	// sleep — ожидание задержки Slow; подменяется в тестах, чтобы обойтись без реального времени.
	sleep func(ctx context.Context, d time.Duration) error

	mu        sync.Mutex
	shipments []Shipment
	seq       int
	faults    Faults
}

// New создаёт перевозчика без заказов и сбоев.
func New(c clock.Clock) *Carrier {
	return &Carrier{clock: c, sleep: timerSleep}
}

// SetSleep подменяет ожидание задержки отправки (для тестов). Вызывать до первой отправки.
func (c *Carrier) SetSleep(fn func(ctx context.Context, d time.Duration) error) {
	c.sleep = fn
}

// Submit создаёт заказ. Повтор с тем же ClientRef создаёт ещё один заказ (нарочно).
// Ошибки: ErrUnavailable, *RefusedError, ErrInvalid, ошибка контекста во время задержки.
func (c *Carrier) Submit(ctx context.Context, o Order) (SubmitResult, error) {
	if err := o.Validate(); err != nil {
		return SubmitResult{}, err
	}
	// Задержка вне блокировки, как в прототипе: иначе сверка второго экземпляра ждала бы первую отправку
	// и окно для дубля бы закрылось.
	if err := c.pause(ctx); err != nil {
		return SubmitResult{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.failIfDown(); err != nil {
		return SubmitResult{}, err
	}
	if c.faults.RefuseNext != "" {
		reason := c.faults.RefuseNext
		c.faults.RefuseNext = ""
		return SubmitResult{}, &RefusedError{Reason: reason}
	}
	c.seq++
	s := Shipment{ShipmentRef: shipmentPrefix + strconv.Itoa(c.seq), Order: o, CreatedAt: c.clock.Now()}
	c.shipments = append(c.shipments, s)
	lost := c.faults.LoseNextSubmitResponse
	c.faults.LoseNextSubmitResponse = false
	return SubmitResult{Shipment: s, ResponseLost: lost}, nil
}

// FindActive возвращает неотменённые заказы по clientRef в порядке создания (сверка шлюза).
func (c *Carrier) FindActive(clientRef string) ([]Shipment, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.failIfDown(); err != nil {
		return nil, err
	}
	out := []Shipment{}
	for _, s := range c.shipments {
		if s.ClientRef == clientRef && !s.Cancelled {
			out = append(out, s)
		}
	}
	return out, nil
}

// CancelByClientRef отменяет все заказы с clientRef, как cancel прототипа. Возвращает все заказы этого
// clientRef после отмены; если заказов нет, пустой список без ошибки (отмена идемпотентна).
func (c *Carrier) CancelByClientRef(clientRef string) ([]Shipment, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.failIfDown(); err != nil {
		return nil, err
	}
	now := c.clock.Now()
	out := []Shipment{}
	for i := range c.shipments {
		if c.shipments[i].ClientRef != clientRef {
			continue
		}
		c.cancelAt(i, now)
		out = append(out, c.shipments[i])
	}
	return out, nil
}

// CancelByShipmentRef отменяет один заказ по номеру перевозчика. Повторная отмена возвращает тот же заказ.
func (c *Carrier) CancelByShipmentRef(ref string) (Shipment, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.failIfDown(); err != nil {
		return Shipment{}, err
	}
	for i := range c.shipments {
		if c.shipments[i].ShipmentRef == ref {
			c.cancelAt(i, c.clock.Now())
			return c.shipments[i], nil
		}
	}
	return Shipment{}, fmt.Errorf("заказ %s: %w", ref, ErrNotFound)
}

// All возвращает все заказы, включая отменённые; непустой clientRef фильтрует. Сбои не действуют:
// это пульт тестов и демо, а не API перевозчика.
func (c *Carrier) All(clientRef string) []Shipment {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []Shipment{}
	for _, s := range c.shipments {
		if clientRef == "" || s.ClientRef == clientRef {
			out = append(out, s)
		}
	}
	return out
}

// Faults возвращает текущие сбои.
func (c *Carrier) Faults() Faults {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.faults
}

// UpdateFaults меняет сбои под блокировкой и возвращает новое состояние.
func (c *Carrier) UpdateFaults(fn func(*Faults)) Faults {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn(&c.faults)
	return c.faults
}

// Reset удаляет заказы и сбои. Нумерация не сбрасывается: номер C03-n не повторяется за жизнь процесса.
func (c *Carrier) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shipments = nil
	c.faults = Faults{}
}

func (c *Carrier) cancelAt(i int, now time.Time) {
	if c.shipments[i].Cancelled {
		return
	}
	c.shipments[i].Cancelled = true
	c.shipments[i].CancelledAt = &now
}

// failIfDown вызывается под блокировкой.
func (c *Carrier) failIfDown() error {
	if c.faults.FailNextCalls > 0 {
		c.faults.FailNextCalls--
		return ErrUnavailable
	}
	return nil
}

func (c *Carrier) pause(ctx context.Context) error {
	c.mu.Lock()
	d := c.faults.Slow
	c.mu.Unlock()
	if d <= 0 {
		return nil
	}
	return c.sleep(ctx, d)
}

// timerSleep — настоящее ожидание; отмена контекста прерывает его, заказ тогда не создаётся.
func timerSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("задержка отправки прервана: %w", ctx.Err())
	}
}
