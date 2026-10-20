// Package clock даёт источник времени, который можно подменить в тестах.
// Сроки подтверждения, таймеры эскалации и водяные знаки зависят от времени,
// поэтому бизнес-код никогда не вызывает time.Now напрямую (CONTRIBUTING.md, правила кода).
package clock

import (
	"sync"
	"time"
)

// Clock возвращает текущее время.
type Clock interface {
	Now() time.Time
}

// System — настоящие часы процесса.
type System struct{}

// Now возвращает time.Now в UTC: все метки времени в системе в UTC.
func (System) Now() time.Time { return time.Now().UTC() }

// Mutable — часы для тестов: время стоит, пока его не сдвинут.
type Mutable struct {
	mu  sync.Mutex
	now time.Time
}

// NewMutable создаёт часы, остановленные на t.
func NewMutable(t time.Time) *Mutable {
	return &Mutable{now: t.UTC()}
}

// Now возвращает установленное время.
func (m *Mutable) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

// Set устанавливает время.
func (m *Mutable) Set(t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = t.UTC()
}

// Advance сдвигает время вперёд на d.
func (m *Mutable) Advance(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = m.now.Add(d)
}
