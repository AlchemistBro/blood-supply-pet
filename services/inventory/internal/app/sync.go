package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
)

// Виды и результаты сеанса обмена (sync_run).
const (
	SyncKindChanges = "CHANGES"
	SyncSuccess     = "SUCCESS"
	SyncPartial     = "PARTIAL"
	SyncFailed      = "FAILED"
	// requestedBySystem — сеанс по расписанию, не по запросу пользователя.
	requestedBySystem = "system"
)

// syncLockKey — advisory-блокировка опроса: один активный опрос на базу, чтобы курсоры сеансов не перемешивались.
const syncLockKey int64 = 0x61697374 // "aist"

// SyncResult — итог сеанса опроса.
type SyncResult struct {
	RunID uuid.UUID
	// Skipped — опрос уже идёт в другом экземпляре.
	Skipped bool
	From    int64
	To      int64
	// Applied — сколько изменений применено (событий в outbox).
	Applied int
	// Rejected — сколько записей источника отброшено как некорректные.
	Rejected int
	// Resynced — АИСТ-М потеряла состояние, опрос начат с 0.
	Resynced bool
	Result   string
}

// RunSync — фоновый воркер опроса АИСТ-М до отмены контекста. Расписание — таймер процесса (time.Timer):
// libs/clock даёт только «который час» для бизнес-времени, а не планировщик; тесты вызывают SyncOnce напрямую.
// Ошибка сеанса не останавливает воркер: она записана в sync_run. Пока АИСТ-М недоступна, пауза между
// сеансами удваивается до MaxPollBackoff, чтобы не плодить FAILED каждые PollInterval; после успеха — снова PollInterval.
func (s *Service) RunSync(ctx context.Context) error {
	delay := s.cfg.PollInterval
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		_, err := s.SyncOnce(ctx)
		if err != nil && ctx.Err() == nil {
			s.log.WarnContext(ctx, "опрос АИСТ-М не удался", "failure", FailureKind(err), "err", err)
		}
		delay = NextPollDelay(delay, s.cfg.PollInterval, s.cfg.MaxPollBackoff, err != nil)
		timer.Reset(delay)
	}
}

// NextPollDelay — пауза до следующего сеанса: после отказа удвоенная предыдущая (не больше max),
// после успеха — обычный интервал.
func NextPollDelay(prev, interval, maxDelay time.Duration, failed bool) time.Duration {
	if !failed {
		return interval
	}
	next := prev * 2
	if next < interval {
		next = interval
	}
	if next > maxDelay {
		next = maxDelay
	}
	return next
}

// SyncOnce — один сеанс CHANGES: страницы изменений от курсора, каждая страница — одна транзакция
// (единицы, история статусов, события в outbox, курсор сеанса). Обрыв посреди сеанса не теряет
// применённых страниц, а повтор страницы ничего не меняет: старая или та же версия не перезаписывает новую.
func (s *Service) SyncOnce(ctx context.Context) (SyncResult, error) {
	lease, err := bspgx.TryAdvisoryLock(ctx, s.pool, syncLockKey)
	if err != nil {
		return SyncResult{}, err
	}
	if lease == nil {
		return SyncResult{Skipped: true}, nil
	}
	defer lease.Release(context.WithoutCancel(ctx))

	cursor, err := s.store.LastSyncCursor(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	runID, err := uuid.NewV7()
	if err != nil {
		return SyncResult{}, fmt.Errorf("uuid v7: %w", err)
	}
	run := SyncRun{ID: runID, Kind: SyncKindChanges, RequestedBy: requestedBySystem, StartedAt: s.clock.Now(), SourceVersion: cursor}
	if err := s.store.StartSyncRun(ctx, run); err != nil {
		return SyncResult{}, err
	}
	res := SyncResult{RunID: runID, From: cursor, To: cursor}
	var note *string
	since := cursor
	complete := false
	for page := 0; page < s.cfg.MaxPagesPerRun; page++ {
		p, err := s.aist.Changes(ctx, since, s.cfg.PageLimit)
		if err != nil {
			return s.fail(ctx, res, pageResult(res), err)
		}
		if p.CurrentVersion < since {
			// АИСТ-М (имитатор без базы) перезапущена: версии снова с 1. Опрос с 0 безопасен —
			// записи, которые уже есть в копии с большей версией, не перезапишут её.
			// Ограничение: перезапуск замечается, только пока источник не успел набрать версий больше
			// нашего курсора. Если до опроса он сделал больше изменений, чем было до перезапуска, курсор
			// выглядит правдоподобно, ранние версии нового источника пропускаются, а единицы со «старыми»
			// версиями (меньше сохранённых) не обновятся до суточной сверки (этап 3, DAILY_SNAPSHOT).
			// Настоящая АИСТ-М версий не сбрасывает; это защита демо-стенда.
			msg := fmt.Sprintf("АИСТ-М потеряла состояние: current_version %d меньше курсора %d, пересинхронизация с 0", p.CurrentVersion, since)
			s.log.WarnContext(ctx, msg, "sync_run_id", runID)
			note = &msg
			res.Resynced = true
			since = 0
			if err := s.store.AdvanceSyncRun(ctx, nil, runID, 0, 0); err != nil {
				return s.fail(ctx, res, pageResult(res), err)
			}
			res.To = 0
			continue
		}
		applied, rejected, err := s.applyPage(ctx, runID, p)
		if err != nil {
			return s.fail(ctx, res, pageResult(res), err)
		}
		res.Applied += applied
		res.Rejected += rejected
		since = p.LastVersion
		res.To = since
		if !p.HasMore {
			complete = true
			break
		}
	}
	res.Result = SyncSuccess
	if !complete {
		// Не догнали источник за сеанс: копия ещё отстаёт, водяной знак не двигаем.
		res.Result = SyncPartial
	}
	if res.Rejected > 0 {
		// Часть записей источника не принята (неизвестная клиника, вид, группа, статус): копия неполна,
		// сеанс PARTIAL и водяной знак не двигается. Курсор при этом идёт дальше, иначе одна плохая
		// запись остановила бы опрос всех остальных; расхождение исправит суточная сверка.
		res.Result = SyncPartial
		msg := fmt.Sprintf("отброшено записей источника: %d", res.Rejected)
		if note != nil {
			msg = *note + "; " + msg
		}
		note = &msg
		s.metrics.rejected.Add(ctx, int64(res.Rejected))
	}
	if err := s.store.FinishSyncRun(ctx, runID, s.clock.Now(), res.Result, note); err != nil {
		return res, err
	}
	if res.Applied > 0 || res.Rejected > 0 {
		s.log.InfoContext(ctx, "опрос АИСТ-М", "sync_run_id", runID, "from", res.From, "to", res.To,
			"applied", res.Applied, "rejected", res.Rejected, "result", res.Result)
	}
	return res, nil
}

func pageResult(res SyncResult) string {
	if res.Applied > 0 {
		return SyncPartial
	}
	return SyncFailed
}

// fail закрывает сеанс с ошибкой. Закрытие делается без отменённого контекста: сеанс не должен
// остаться открытым из-за остановки сервиса.
func (s *Service) fail(ctx context.Context, res SyncResult, result string, cause error) (SyncResult, error) {
	res.Result = result
	msg := cause.Error()
	if err := s.store.FinishSyncRun(context.WithoutCancel(ctx), res.RunID, s.clock.Now(), result, &msg); err != nil {
		return res, errors.Join(cause, err)
	}
	return res, cause
}

// applyPage применяет страницу одной транзакцией и двигает курсор сеанса.
func (s *Service) applyPage(ctx context.Context, runID uuid.UUID, p ChangesPage) (applied, rejected int, err error) {
	now := s.clock.Now()
	err = bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		applied, rejected = 0, 0
		for _, au := range p.Units {
			src, err := s.cat.source(au)
			if err == nil {
				err = src.Validate()
			}
			if err != nil {
				// Некорректная запись источника не должна стопорить опрос всех остальных единиц:
				// она пропускается с предупреждением (без кода единицы и фенотипа в логе).
				rejected++
				s.log.WarnContext(ctx, "запись АИСТ-М отброшена", "version", au.Version, "err", err)
				continue
			}
			ok, err := s.applyUnit(ctx, tx, src, now)
			if err != nil {
				return err
			}
			if ok {
				applied++
			}
		}
		return s.store.AdvanceSyncRun(ctx, tx, runID, p.LastVersion, applied)
	})
	return applied, rejected, err
}

// applyUnit применяет одну запись под блокировкой строки единицы. true — копия изменилась и событие записано.
func (s *Service) applyUnit(ctx context.Context, tx pgx.Tx, src domain.SourceUnit, now time.Time) (bool, error) {
	for attempt := 0; attempt < 2; attempt++ {
		prev, err := s.store.LockUnitByCode(ctx, tx, src.AistUnitCode)
		if err != nil {
			return false, err
		}
		newID, err := uuid.NewV7()
		if err != nil {
			return false, fmt.Errorf("uuid v7: %w", err)
		}
		ch, err := domain.Apply(prev, src, newID, now)
		if err != nil {
			return false, err
		}
		switch ch.Event {
		case domain.EventNone:
			return false, nil
		case domain.EventRegistered:
			inserted, err := s.store.InsertUnit(ctx, tx, ch.Unit)
			if err != nil {
				return false, err
			}
			if !inserted {
				continue // единицу только что вставил другой: перечитать под блокировкой и решить заново
			}
		default:
			updated, err := s.store.UpdateUnitIfNewer(ctx, tx, ch.Unit)
			if err != nil {
				return false, err
			}
			if !updated {
				return false, nil
			}
		}
		if ch.StatusChanged {
			hid, err := uuid.NewV7()
			if err != nil {
				return false, fmt.Errorf("uuid v7: %w", err)
			}
			if err := s.store.InsertStatusHistory(ctx, tx, hid, ch.Unit, now); err != nil {
				return false, err
			}
		}
		if err := s.pub.PublishUnit(ctx, tx, ch); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, fmt.Errorf("единица версии %d: конкурентная вставка не разрешилась", src.Version)
}
