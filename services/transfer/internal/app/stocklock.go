package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// stockKey — строка Остатка (клиника × вид).
type stockKey struct {
	clinicID int64
	kind     string
}

func compareStockKeys(a, b stockKey) int {
	if c := cmp.Compare(a.clinicID, b.clinicID); c != 0 {
		return c
	}
	return cmp.Compare(a.kind, b.kind)
}

// errUnitMovedConcurrently — пока событие ждало блокировки, изменилась область затронутых Остатков.
// Ошибка временная: consumer повторит событие, и оно возьмёт уже верные строки.
var errUnitMovedConcurrently = errors.New("единица сменила Остаток или закрепление во время обработки события")

// unitScope — какие строки Остатка затрагивает событие единицы: Остаток из события, Остаток копии
// (единица могла сменить клинику или вид) и Остаток источника обязательства, за которым единица закреплена.
type unitScope struct {
	copyKey *stockKey
	keys    []stockKey
}

// lockUnitScope блокирует все затронутые строки Остатка до строки обязательства, по возрастанию ключа:
// обработчики, которым нужна одна строка, и обработчики, которым нужны две, не ждут друг друга по кругу.
// Затем перечитывает копию и закрепление: изменение набора нужных строк требует повтора.
func (s *Service) lockUnitScope(ctx context.Context, tx pgx.Tx, unitID uuid.UUID, event stockKey) (unitScope, error) {
	before, err := s.readUnitScope(ctx, tx, unitID, event)
	if err != nil {
		return unitScope{}, err
	}
	for _, k := range before.keys {
		if _, err := s.store.LockStock(ctx, tx, k.clinicID, k.kind); err != nil {
			return unitScope{}, err
		}
	}
	after, err := s.readUnitScope(ctx, tx, unitID, event)
	if err != nil {
		return unitScope{}, err
	}
	if !slices.Equal(before.keys, after.keys) {
		return unitScope{}, fmt.Errorf("единица %s: %w", unitID, errUnitMovedConcurrently)
	}
	// Новое закрепление в том же Остатке уже защищено взятой блокировкой. Обрабатываем актуального
	// владельца; повтор нужен лишь тогда, когда для новой области не хватает заблокированных строк.
	if after.copyKey != nil && *after.copyKey != event {
		s.log.WarnContext(ctx, "клиника или вид в событии расходятся с копией; заблокированы оба остатка",
			"unit_id", unitID, "event_clinic_id", event.clinicID, "copy_clinic_id", after.copyKey.clinicID)
	}
	return after, nil
}

func (s *Service) readUnitScope(ctx context.Context, tx pgx.Tx, unitID uuid.UUID, event stockKey) (unitScope, error) {
	sc := unitScope{keys: []stockKey{event}}
	copies, err := s.store.UnitCopies(ctx, tx, []uuid.UUID{unitID})
	if err != nil {
		return unitScope{}, err
	}
	if c, ok := copies[unitID]; ok {
		k := stockKey{clinicID: c.ClinicID, kind: c.ComponentKind}
		sc.copyKey = &k
		sc.keys = append(sc.keys, k)
	}
	obligationID, held, err := s.store.ObligationHoldingUnit(ctx, tx, unitID)
	if err != nil {
		return unitScope{}, err
	}
	if held {
		o, err := s.store.Obligation(ctx, tx, obligationID)
		if err != nil {
			return unitScope{}, err
		}
		sc.keys = append(sc.keys, stockKey{clinicID: o.SourceClinicID, kind: o.ComponentKind})
	}
	slices.SortFunc(sc.keys, compareStockKeys)
	sc.keys = slices.Compact(sc.keys)
	return sc, nil
}
