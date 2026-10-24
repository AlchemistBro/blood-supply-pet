package domain

import (
	"errors"
	"fmt"
)

// Status — статус обязательства O07 (docs/domain/obligation-states.md). Других статусов нет:
// частичное подтверждение — это confirmed_qty < requested_qty, «ожидает замены» нет (решение владельца 23.10.2026).
type Status string

// Статусы O07.
const (
	StatusPending            Status = "PENDING"
	StatusConfirmed          Status = "CONFIRMED"
	StatusTransportRequested Status = "TRANSPORT_REQUESTED"
	StatusDispatched         Status = "DISPATCHED"
	StatusDelivered          Status = "DELIVERED"
	StatusRejected           Status = "REJECTED"
	StatusExpired            Status = "EXPIRED"
	StatusCancelled          Status = "CANCELLED"
	StatusBroken             Status = "BROKEN"
)

// AllStatuses — все статусы в порядке схемы 13.
var AllStatuses = []Status{
	StatusPending, StatusConfirmed, StatusTransportRequested, StatusDispatched, StatusDelivered,
	StatusRejected, StatusExpired, StatusCancelled, StatusBroken,
}

// transitions — разрешённые переходы. Переход в себя у CONFIRMED и TRANSPORT_REQUESTED — замена единицы (ADR-0011).
var transitions = map[Status][]Status{
	StatusPending:            {StatusConfirmed, StatusRejected, StatusExpired, StatusCancelled},
	StatusConfirmed:          {StatusTransportRequested, StatusCancelled, StatusBroken, StatusConfirmed},
	StatusTransportRequested: {StatusCancelled, StatusBroken, StatusTransportRequested, StatusDispatched},
	StatusDispatched:         {StatusDelivered},
}

// ErrTransitionNotAllowed — переход отсутствует в схеме состояний.
var ErrTransitionNotAllowed = errors.New("переход статуса обязательства не разрешён")

// ParseStatus разбирает статус из БД или сообщения.
func ParseStatus(s string) (Status, error) {
	for _, st := range AllStatuses {
		if string(st) == s {
			return st, nil
		}
	}
	return "", fmt.Errorf("неизвестный статус обязательства %q", s)
}

// CanTransition сообщает, есть ли переход from → to в схеме состояний.
func CanTransition(from, to Status) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// CheckTransition — то же, что CanTransition, но с ошибкой для сценариев приложения.
func CheckTransition(from, to Status) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("%w: %s → %s", ErrTransitionNotAllowed, from, to)
	}
	return nil
}

// IsTerminal — конечный статус: дальше переходов нет.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusRejected, StatusExpired, StatusCancelled, StatusBroken, StatusDelivered:
		return true
	default:
		return false
	}
}

// HoldsAllocations — статус держит действующие закрепления единиц (O18 CONFIRMED).
func (s Status) HoldsAllocations() bool {
	return s == StatusConfirmed || s == StatusTransportRequested
}

// Replaceable — замена единицы разрешена: подтверждено и ещё не отправлено (ADR-0011).
func (s Status) Replaceable() bool { return s.HoldsAllocations() }

// Breakable — выбытие или карантин закреплённой единицы переводит O07 в BROKEN.
func (s Status) Breakable() bool { return s.HoldsAllocations() }

// Cancellable — отмена заявки или пересчёт плана переводит O07 в CANCELLED.
// После DISPATCHED отмена уже не компенсация: возврат решает ОПК (obligation-states.md, правило 5).
func (s Status) Cancellable() bool {
	return s == StatusPending || s.HoldsAllocations()
}

// StatusesCancellable — ожидаемые статусы для CAS отмены.
func StatusesCancellable() []Status { return filter(Status.Cancellable) }

// StatusesBreakable — ожидаемые статусы для CAS нарушения.
func StatusesBreakable() []Status { return filter(Status.Breakable) }

func filter(fn func(Status) bool) []Status {
	var out []Status
	for _, s := range AllStatuses {
		if fn(s) {
			out = append(out, s)
		}
	}
	return out
}
