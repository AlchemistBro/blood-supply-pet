package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

// Replace — замена закреплённой единицы равноценной до отправки (ADR-0011). Равноценность задаёт спецификация
// из выбора врача; кандидат проверяется по копии и тем же точечным вызовом в АИСТ-М, что и при подтверждении.
// В транзакции: блокировка Остатка, затем O07; старое закрепление RELEASED, новое CONFIRMED под уникальным
// индексом; резерв и команда перевозчику не меняются (в RequestTransport нет номеров единиц).
func (s *Service) Replace(ctx context.Context, c ReplaceUnit) (domain.ReplacementResult, error) {
	o, err := s.store.Obligation(ctx, s.pool, c.ObligationID)
	if err != nil {
		return nil, err
	}
	if !o.Status.Replaceable() {
		return domain.NotAllowed{Status: o.Status}, nil
	}
	allocated, err := s.store.IsAllocatedTo(ctx, s.pool, o.ID, c.OldUnitID)
	if err != nil {
		return nil, err
	}
	if !allocated {
		return domain.SpecMismatch{Reason: domain.MismatchNotAllocated}, nil
	}
	copies, err := s.store.UnitCopies(ctx, s.pool, []uuid.UUID{c.NewUnitID})
	if err != nil {
		return nil, err
	}
	candidate, ok := copies[c.NewUnitID]
	if !ok {
		return domain.SpecMismatch{Reason: domain.MismatchUnknownUnit}, nil
	}
	if reason := o.Spec.Mismatch(o.SourceClinicID, o.ComponentKind, candidate); reason != "" {
		return domain.SpecMismatch{Reason: reason}, nil
	}
	// Карантин по копии — сразу отказ: такую единицу не закрепляем (ПП РФ № 641, п. 62 а).
	if !candidate.Status.Allocatable() {
		return domain.UnitUnavailable{UnitID: c.NewUnitID}, nil
	}

	report, err := s.checker.CheckUnits(ctx, []uuid.UUID{c.NewUnitID})
	if err != nil || report.FromCopy {
		s.log.WarnContext(ctx, "статус кандидата замены в АИСТ-М не получен", "obligation_id", o.ID, "err", err)
		return domain.SourceSystemUnavailable{}, nil
	}
	if !report.Statuses[c.NewUnitID].Allocatable() {
		return domain.UnitUnavailable{UnitID: c.NewUnitID}, nil
	}

	var res domain.ReplacementResult
	err = s.inTx(ctx, nil, func(ctx context.Context, tx pgx.Tx) error {
		r, err := s.replaceLocked(ctx, tx, o, c)
		res = r
		return err
	})
	if errors.Is(err, ErrUnitTaken) {
		return domain.UnitTaken{UnitID: c.NewUnitID}, nil
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Service) replaceLocked(ctx context.Context, tx pgx.Tx, o domain.Obligation, c ReplaceUnit) (domain.ReplacementResult, error) {
	now := s.clock.Now()
	if _, err := s.store.LockStock(ctx, tx, o.SourceClinicID, o.ComponentKind); err != nil {
		return nil, err
	}
	locked, err := s.store.LockObligation(ctx, tx, o.ID)
	if err != nil {
		return nil, err
	}
	// Ответ TransportAccepted мог перевести O07 дальше — это тоже допустимо; нарушение или отмена — нет.
	if !locked.Status.Replaceable() {
		return domain.NotAllowed{Status: locked.Status}, nil
	}
	allocated, err := s.store.IsAllocatedTo(ctx, tx, o.ID, c.OldUnitID)
	if err != nil {
		return nil, err
	}
	if !allocated {
		return domain.SpecMismatch{Reason: domain.MismatchNotAllocated}, nil
	}
	notInStock, err := s.store.NotInStockAmong(ctx, tx, []uuid.UUID{c.NewUnitID})
	if err != nil {
		return nil, err
	}
	if len(notInStock) > 0 {
		return domain.UnitUnavailable{UnitID: c.NewUnitID}, nil
	}

	// Под блокировкой O07 оба изменения обязаны пройти; иначе откат всей транзакции.
	touched, err := s.store.CASTouch(ctx, tx, locked, now)
	if err != nil {
		return nil, err
	}
	released, err := s.store.ReleaseAllocation(ctx, tx, o.ID, c.OldUnitID, now)
	if err != nil {
		return nil, err
	}
	if !touched || !released {
		return nil, fmt.Errorf("замена в %s: состояние изменилось под блокировкой", o.ID)
	}
	if err := s.store.InsertAllocations(ctx, tx, o.ID, []uuid.UUID{c.NewUnitID}, now); err != nil {
		return nil, err
	}
	if err := s.event(ctx, tx, o.ID, TransferUnitReplaced{
		ObligationID: o.ID, OldUnitID: c.OldUnitID, NewUnitID: c.NewUnitID, RuleVersion: o.Spec.RuleVersion,
	}); err != nil {
		return nil, err
	}
	if err := s.audit(ctx, tx, c.Operator, ActionUnitReplaced, o, map[string]any{
		"old_unit_id": c.OldUnitID, "new_unit_id": c.NewUnitID, "rule_version": o.Spec.RuleVersion,
	}); err != nil {
		return nil, err
	}
	return domain.Replaced{ObligationID: o.ID, OldUnitID: c.OldUnitID, NewUnitID: c.NewUnitID}, nil
}
