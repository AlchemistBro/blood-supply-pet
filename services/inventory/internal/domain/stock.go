package domain

import (
	"bytes"
	"math"
	"sort"
	"time"
)

// Usable — единица входит в фактический запас (O02) в момент at: статус доступен по справочнику
// ref_unit_status (карантин недоступен, ПП РФ № 641, п. 62) и срок годности не истёк (FR007).
// Годность до at включительно уже истекла: единица с expires_at = at в запас не входит.
func Usable(statusAvailable bool, expiresAt, at time.Time) bool {
	return statusAvailable && expiresAt.After(at)
}

// SortFEFO упорядочивает единицы по правилу FEFO (FR015): сначала с ближайшей годностью,
// при равной годности — по unit_id, чтобы порядок был воспроизводимым. Тот же порядок задаёт SQL списка единиц.
func SortFEFO(units []Unit) {
	sort.SliceStable(units, func(i, j int) bool {
		if !units[i].ExpiresAt.Equal(units[j].ExpiresAt) {
			return units[i].ExpiresAt.Before(units[j].ExpiresAt)
		}
		return bytes.Compare(units[i].UnitID[:], units[j].UnitID[:]) < 0
	})
}

// Litres переводит миллилитры в литры (FR011): сумма объёмов хранится в целых миллилитрах, поэтому
// результат точен до третьего знака; округление до миллилитра делается явно, как numeric(…, 3) в ПР6.
func Litres(ml int64) float64 {
	return math.Round(float64(ml)/1000*1000) / 1000
}

// Stale — копия запаса устарела (NFR015): успешного обновления не было или оно старше порога.
// Порог — параметр, согласуемый с заказчиком (ADR-0010: демо-значение задаёт конфигурация).
func Stale(watermark *time.Time, at time.Time, limit time.Duration) bool {
	if watermark == nil {
		return true
	}
	return at.Sub(*watermark) > limit
}
