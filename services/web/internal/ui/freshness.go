package ui

import (
	"context"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
)

// Freshness — водяной знак запаса в шапке (NFR015): когда запас последний раз успешно обновлён из C01
// и не превышен ли порог актуальности.
type Freshness struct {
	Known     bool          // время известно; иначе «Время обновления запаса неизвестно»
	UpdatedAt time.Time     // последнее успешное обновление, UTC
	Age       time.Duration // сколько прошло к моменту рендера
	Stale     bool          // порог превышен или время неизвестно: жёлтая полоса под шапкой
	ShowDate  bool          // обновление было в прошлые сутки по Москве: показывать дату, а не только время
}

// FreshnessSource — источник времени последнего обновления запаса (inventory GET /sync/last).
// Нулевое время без ошибки — синхронизаций ещё не было.
type FreshnessSource interface {
	LastStockSync(ctx context.Context) (time.Time, error)
}

// NewFreshness считает водяной знак. staleAfter — порог актуальности (параметр, согласуемый с заказчиком, NFR015);
// 0 — предупреждение по возрасту не показывается. Время из будущего (рассинхрон часов) считается «только что».
func NewFreshness(updatedAt, now time.Time, staleAfter time.Duration) Freshness {
	if updatedAt.IsZero() {
		return UnknownFreshness()
	}
	age := now.Sub(updatedAt)
	if age < 0 {
		age = 0
	}
	u, n := updatedAt.In(Moscow), now.In(Moscow)
	return Freshness{
		Known:     true,
		UpdatedAt: updatedAt.UTC(),
		Age:       age,
		Stale:     staleAfter > 0 && age > staleAfter,
		ShowDate:  u.Year() != n.Year() || u.YearDay() != n.YearDay(),
	}
}

// UnknownFreshness — время обновления неизвестно (сервис запаса не ответил или синхронизаций не было).
// Это тоже повод предупредить: свежесть запаса не доказана.
func UnknownFreshness() Freshness {
	return Freshness{Stale: true}
}

// LoadFreshness спрашивает источник и считает водяной знак по часам clk. Ошибка источника не ломает страницу:
// показывается «неизвестно» с предупреждением.
func LoadFreshness(ctx context.Context, src FreshnessSource, clk clock.Clock, staleAfter time.Duration) Freshness {
	at, err := src.LastStockSync(ctx)
	if err != nil {
		return UnknownFreshness()
	}
	return NewFreshness(at, clk.Now(), staleAfter)
}
