package app

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
)

type SnapshotSource interface {
	Snapshot(ctx context.Context) ([]AistUnit, error)
}

func (s *Service) completeDataSync(ctx context.Context, tx pgx.Tx, run SyncRun, res SyncResult, discrepancies int) error {
	at := s.clock.Now()
	// Холостой опрос изменений (ничего не применено, расхождений нет) прогон фиксирует, а полный снимок запаса
	// не публикует: опрос идёт каждые пять секунд, и без этого outbox и проекции подписчиков растут без предела.
	// Водяной знак (NFR015) двигается по самой записи успешного прогона.
	idle := run.Kind == "CHANGES" && res.Applied == 0 && discrepancies == 0
	items, err := s.store.CompleteDataSync(ctx, tx, run.ID, at, res.Result, discrepancies, run.Note, !idle)
	if err != nil {
		return err
	}
	for _, item := range items {
		if _, err := s.journal.Publish(ctx, tx, contracts.TopicInventoryEvents, fmt.Sprintf("%d:%s", item.ClinicID, item.ComponentKind), item); err != nil {
			return err
		}
	}
	if _, err := s.journal.Publish(ctx, tx, contracts.TopicInventoryEvents, run.ID.String(), inventory.SyncRunCompleted{SyncRunID: run.ID, Kind: run.Kind, Result: res.Result, ClinicID: run.ClinicID, ChangesCount: res.Applied, DiscrepanciesCount: &discrepancies, StartedAt: &run.StartedAt, FinishedAt: at}); err != nil {
		return err
	}
	return s.journal.Audit(ctx, tx, messaging.AuditEntry{UserID: run.RequestedBy, ClinicID: run.ClinicID, Action: "SYNC_COMPLETED", ObjectType: "sync_run", ObjectID: run.ID.String(), Result: auditc.ResultSuccess, Details: map[string]any{"kind": run.Kind, "result": res.Result, "changes": res.Applied, "discrepancies": discrepancies}})
}

// SyncSnapshot читает и проверяет CSV до транзакции; исправления, аудит и снимки запаса фиксируются вместе.
func (s *Service) SyncSnapshot(ctx context.Context) (SyncResult, error) {
	lease, err := bspgx.TryAdvisoryLock(ctx, s.pool, syncLockKey)
	if err != nil {
		return SyncResult{}, err
	}
	if lease == nil {
		return SyncResult{Skipped: true}, nil
	}
	defer lease.Release(context.WithoutCancel(ctx))
	id, err := uuid.NewV7()
	if err != nil {
		return SyncResult{}, err
	}
	run := SyncRun{ID: id, Kind: "DAILY_SNAPSHOT", RequestedBy: requestedBySystem, StartedAt: s.clock.Now()}
	if err := s.store.StartSyncRun(ctx, run); err != nil {
		return SyncResult{}, err
	}
	res := SyncResult{RunID: id, Result: SyncSuccess}
	source, ok := s.aist.(SnapshotSource)
	if !ok {
		return s.fail(ctx, res, SyncFailed, fmt.Errorf("снимок АИСТ-М не настроен"))
	}
	rows, err := source.Snapshot(ctx)
	if err != nil {
		return s.fail(ctx, res, SyncFailed, err)
	}
	units := make([]domain.SourceUnit, 0, len(rows))
	codes := make([]string, 0, len(rows))
	for i, row := range rows {
		u, err := s.cat.source(row)
		if err != nil {
			return s.fail(ctx, res, SyncFailed, fmt.Errorf("снимок: запись %d не соответствует справочникам", i+1))
		}
		if err := u.Validate(); err != nil {
			return s.fail(ctx, res, SyncFailed, fmt.Errorf("снимок: запись %d некорректна", i+1))
		}
		units = append(units, u)
		codes = append(codes, u.AistUnitCode)
	}
	err = bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		discrepancies := 0
		for _, u := range units {
			prev, err := s.store.LockUnitByCode(ctx, tx, u.AistUnitCode)
			if err != nil {
				return err
			}
			newID, err := uuid.NewV7()
			if err != nil {
				return err
			}
			ch, kind, err := domain.Reconcile(prev, u, newID, s.clock.Now())
			if err != nil {
				return err
			}
			if kind == domain.DiscrepancyNone {
				continue
			}
			discrepancies++
			if prev == nil {
				inserted, err := s.store.InsertUnit(ctx, tx, ch.Unit)
				if err != nil {
					return err
				}
				if !inserted {
					return fmt.Errorf("конкурентная вставка во время сверки")
				}
			} else if err := s.store.CorrectUnitFromSnapshot(ctx, tx, ch.Unit); err != nil {
				return err
			}
			if ch.StatusChanged {
				hid, err := uuid.NewV7()
				if err != nil {
					return err
				}
				if err := s.store.InsertStatusHistory(ctx, tx, hid, ch.Unit, s.clock.Now()); err != nil {
					return err
				}
			}
			if err := s.pub.PublishUnit(ctx, tx, ch); err != nil {
				return err
			}
			if err := s.journal.Audit(ctx, tx, messaging.AuditEntry{UserID: requestedBySystem, Action: "SNAPSHOT_DISCREPANCY", ObjectType: "unit", ObjectID: ch.Unit.UnitID.String(), Result: auditc.ResultSuccess, Details: map[string]any{"kind": string(kind), "from": ch.From, "to": ch.Unit.Status}}); err != nil {
				return err
			}
			res.Applied++
		}
		missing, err := s.store.SnapshotMissingUnits(ctx, tx, codes)
		if err != nil {
			return err
		}
		for _, id := range missing {
			discrepancies++
			if err := s.journal.Audit(ctx, tx, messaging.AuditEntry{UserID: requestedBySystem, Action: "SNAPSHOT_DISCREPANCY", ObjectType: "unit", ObjectID: id.String(), Result: auditc.ResultSuccess, Details: map[string]any{"kind": string(domain.DiscrepancyAbsentInSource)}}); err != nil {
				return err
			}
		}
		if len(missing) > 0 {
			res.Result = SyncPartial
		}
		if err := s.store.AdvanceSyncRun(ctx, tx, run.ID, 0, res.Applied); err != nil {
			return err
		}
		if err := s.checkNorms(ctx, tx); err != nil {
			return err
		}
		return s.completeDataSync(ctx, tx, run, res, discrepancies)
	})
	if err != nil {
		return s.fail(ctx, res, SyncFailed, err)
	}
	return res, nil
}

func (s *Service) RunSnapshots(ctx context.Context) error {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		res, err := s.SyncSnapshot(ctx)
		if err != nil && ctx.Err() == nil {
			s.log.WarnContext(ctx, "суточная сверка АИСТ-М не удалась", "failure", FailureKind(err))
		}
		delay := 24 * time.Hour
		if err != nil || res.Skipped {
			delay = time.Minute
		}
		timer.Reset(delay)
	}
}
