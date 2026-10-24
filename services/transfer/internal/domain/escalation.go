package domain

import (
	"time"

	"github.com/google/uuid"
)

// EscalationNamespace — пространство имён uuid v5 для choice_id заданий, открытых таймером эскалации.
// Значение зафиксировано навсегда: при смене повтор эскалации уже истёкшего O07 открыл бы второе задание.
var EscalationNamespace = uuid.MustParse("6f1c3a52-9b0e-4d57-8a43-1e2b7c9d0f15")

// EscalationChoiceID — choice_id нового O07 у следующего источника: uuid5(пространство, "escalation:" + старый id).
// Повтор таймера упирается в UNIQUE (choice_id) и не создаёт второе задание.
func EscalationChoiceID(expired uuid.UUID) uuid.UUID {
	return uuid.NewSHA1(EscalationNamespace, []byte("escalation:"+expired.String()))
}

// NextSource — следующий источник из fallback_sources и хвост для нового задания.
// Получатель источником быть не может (CHECK source ≠ target), такие элементы пропускаются.
func NextSource(fallback []int64, target int64) (next int64, rest []int64, ok bool) {
	for i, c := range fallback {
		if c == target {
			continue
		}
		rest = append([]int64{}, fallback[i+1:]...)
		return c, rest, true
	}
	return 0, nil, false
}

// Deadline — срок подтверждения: момент открытия плюс окно срочности (FR049).
func Deadline(now time.Time, window time.Duration) time.Time {
	return now.Add(window)
}
