package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

const reasonDeadline = "DEADLINE"

// ExpireOverdue — таймер срока подтверждения (FR049). Для каждого просроченного PENDING в одной транзакции:
// CAS PENDING → EXPIRED, TransferExpired, аудит; новое O07 у следующего источника из fallback_sources
// (choice_id = uuid5 от старого id) или EscalationRaised(EXPIRED_NO_FALLBACK), если источников нет.
// Позднее подтверждение первого источника проигрывает CAS: две отправки невозможны.
// Блокируется только строка O07: Остаток таймер не трогает.
// Ошибка по одному заданию не останавливает остальные: его транзакция откатывается, оно остаётся PENDING
// и пробуется на следующем проходе; ошибки идут в лог, в метрику и в возвращаемую ошибку.
func (s *Service) ExpireOverdue(ctx context.Context) ([]Escalation, error) {
	ids, err := s.store.PendingDueBy(ctx, s.pool, s.clock.Now(), s.cfg.ExpiryBatch)
	if err != nil {
		return nil, err
	}
	var out []Escalation
	var failures []error
	for _, id := range ids {
		var esc *Escalation
		err := s.inTx(ctx, nil, func(ctx context.Context, tx pgx.Tx) error {
			e, err := s.expire(ctx, tx, id)
			esc = e
			return err
		})
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			s.metrics.expiryFailed(ctx)
			s.log.ErrorContext(ctx, "истечение обязательства не удалось, повтор на следующем проходе", "obligation_id", id, "err", err)
			failures = append(failures, fmt.Errorf("истечение %s: %w", id, err))
			continue
		}
		if esc != nil {
			out = append(out, *esc)
		}
	}
	return out, errors.Join(failures...)
}

func (s *Service) expire(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*Escalation, error) {
	now := s.clock.Now()
	before, err := s.store.Obligation(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := s.store.LockRequest(ctx, tx, before.RequestID); err != nil {
		return nil, err
	}
	o, err := s.store.LockObligation(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if o.Status != domain.StatusPending || o.Deadline.After(now) {
		return nil, nil // оператор успел подтвердить, или задание уже снято
	}
	ok, err := s.store.CASStatus(ctx, tx, o, domain.StatusExpired, reasonDeadline, now)
	if err != nil || !ok {
		return nil, err
	}

	esc := &Escalation{ExpiredID: o.ID}
	if next, rest, has := domain.NextSource(o.FallbackSources, o.TargetClinicID); has {
		nextID, _, err := s.openTx(ctx, tx, domain.NewObligation{
			ChoiceID: domain.EscalationChoiceID(o.ID), RequestID: o.RequestID, Origin: o.Origin,
			SourceClinicID: next, TargetClinicID: o.TargetClinicID, ComponentKind: o.ComponentKind,
			RequestedQty: o.RequestedQty, UrgencyCode: o.UrgencyCode, ConfirmWindow: o.ConfirmWindow,
			FallbackSources: rest, Spec: o.Spec, ProtectedFloors: o.ProtectedFloors,
		}, actorSystem)
		if err != nil {
			return nil, err
		}
		if nextID != uuid.Nil {
			esc.NextID = &nextID
		}
	}

	r := refsAt(o, o.Version+1)
	if err := s.event(ctx, tx, o.ID, TransferExpired{
		ObligationID: o.ID, ObligationVersion: r.version, SourceClinicID: r.source, TargetClinicID: r.target,
		RequestID: o.RequestID, NextObligationID: esc.NextID,
	}); err != nil {
		return nil, err
	}
	details := map[string]any{"escalation": "дежурный смены"}
	if esc.NextID != nil {
		details["next_obligation_id"] = esc.NextID.String()
	}
	if err := s.audit(ctx, tx, actorSystem, ActionExpired, o, details); err != nil {
		return nil, err
	}
	if esc.NextID == nil {
		if err := s.event(ctx, tx, o.ID, EscalationRaised{
			ObligationID: o.ID, ObligationVersion: r.version, SourceClinicID: r.source, TargetClinicID: r.target, RequestID: o.RequestID, Reason: string(domain.EscalationExpiredNoFallback), At: now,
		}); err != nil {
			return nil, err
		}
	}
	return esc, nil
}

// RunExpiryTimer — фоновый воркер таймера: опрос каждые cfg.ExpiryInterval до отмены ctx.
// Тикер задаёт только частоту опроса; «просрочено ли» решает Clock. Несколько экземпляров безопасны: истечение — CAS.
func (s *Service) RunExpiryTimer(ctx context.Context) error {
	t := time.NewTicker(s.cfg.ExpiryInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			escalations, err := s.ExpireOverdue(ctx)
			if err != nil {
				s.log.WarnContext(ctx, "таймер срока подтверждения: ошибка, повтор на следующем тике", "err", err)
			}
			for _, e := range escalations {
				s.log.InfoContext(ctx, "обязательство истекло", "obligation_id", e.ExpiredID, "next_obligation_id", e.NextID)
			}
		}
	}
}
