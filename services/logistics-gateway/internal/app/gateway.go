// Package app — сценарии шлюза перевозчика: приём команд из logistics.commands, продвижение заказа (drive)
// и сверщик. Перенос TransportGateway и Reconciler прототипа.
//
// Порядок работы с C03 (saga-confirmation.md, таблица сбоев шлюза):
//  1. Команда, отметка в inbox и строка REQUESTING — одна транзакция; вызова C03 в ней нет.
//  2. Drive берёт аренду строки, сверяется с C03 по clientRef и только если заказа нет — отправляет.
//  3. Исход и ответ в outbox пишутся одной транзакцией и только пока аренда наша (CAS по lease_owner).
//  4. Упал посреди запроса — строка остаётся REQUESTING, аренда истекает, сверщик повторяет тот же Drive.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/domain"
)

// Store — порт хранилища заказов перевозки.
type Store interface {
	// Insert записывает строку REQUESTING; false — строка по обязательству уже есть (повтор или надгробие).
	Insert(ctx context.Context, tx pgx.Tx, req domain.Request, now time.Time) (bool, error)
	// RequestCancel ставит флаг отмены или вставляет надгробие. existed=false — строки не было.
	RequestCancel(ctx context.Context, tx pgx.Tx, obligationID uuid.UUID, now time.Time) (before domain.Status, existed bool, err error)
	// Claim берёт аренду строки, если ей есть что делать с C03 и её никто не держит.
	Claim(ctx context.Context, obligationID, owner uuid.UUID, now, until time.Time) (domain.Row, bool, error)
	// Finish записывает исход, только если аренда всё ещё у owner и статус равен expected (CAS).
	// cancelRequested — флаг отмены на момент записи: отмена могла прийти, пока шли вызовы C03.
	Finish(ctx context.Context, tx pgx.Tx, obligationID, owner uuid.UUID, expected, status domain.Status, shipmentRef string,
		now time.Time) (finished, cancelRequested bool, err error)
	// FailAttempt засчитывает неудачную попытку, только если аренда всё ещё у owner и статус равен expected.
	FailAttempt(ctx context.Context, tx pgx.Tx, obligationID, owner uuid.UUID, expected, status domain.Status, lastError string,
		nextAttemptAt, now time.Time) (bool, error)
	// Release отпускает аренду owner без исхода и без засчитанной попытки.
	Release(ctx context.Context, obligationID, owner uuid.UUID, now time.Time) error
	// Due — строки, которым пора к C03, по next_attempt_at.
	Due(ctx context.Context, now time.Time, limit int) ([]uuid.UUID, error)
	// Find читает строку.
	Find(ctx context.Context, obligationID uuid.UUID) (domain.Row, bool, error)
}

// Order — заказ перевозки для C03.
type Order struct {
	ClientRef string
	domain.Request
}

// SubmitResult — ответ C03 на отправку: принят (ShipmentRef) или отказ (Refused, Reason).
type SubmitResult struct {
	ShipmentRef string
	Refused     bool
	Reason      string
}

// ErrCarrierUnavailable — C03 недоступна или исход вызова неизвестен (обрыв, таймаут, 5xx). Отправка могла дойти,
// поэтому повтор идёт только через сверку.
var ErrCarrierUnavailable = errors.New("C03 недоступна или исход вызова неизвестен")

// Carrier — порт перевозчика C03. Ошибки, кроме отмены контекста, оборачивают ErrCarrierUnavailable.
type Carrier interface {
	// FindActive — номер активного заказа по clientRef; пусто — заказа нет.
	FindActive(ctx context.Context, clientRef string) (string, error)
	// Submit отправляет заказ. Не идемпотентен: повтор создаёт второй заказ.
	Submit(ctx context.Context, o Order) (SubmitResult, error)
	// Cancel снимает все заказы clientRef; отсутствие заказов не ошибка.
	Cancel(ctx context.Context, clientRef string) error
}

// Outbox — порт исходящих сообщений в транзакции: ответы transfer и аудит.
type Outbox interface {
	Reply(ctx context.Context, tx pgx.Tx, r domain.Reply) error
	Audit(ctx context.Context, tx pgx.Tx, e messaging.AuditEntry) error
}

// Settings — параметры шлюза (BS_LOGISTICS_*), значения по умолчанию из прототипа.
//
// Допущение (как в прототипе): аренда сравнивается по часам экземпляра, который её берёт или проверяет.
// Экземпляры обязаны синхронизировать время (NTP), а расхождение часов должно быть много меньше запаса
// «аренда − 2 × таймаут вызова C03» (около 20 с при значениях по умолчанию). Иначе экземпляр с убежавшими
// вперёд часами сочтёт чужую аренду истёкшей и заговорит с C03 одновременно с владельцем.
type Settings struct {
	// Lease — аренда строки; должна быть дольше всех вызовов C03 одного Drive (до трёх таймаутов клиента).
	Lease time.Duration
	// CallTimeout — таймаут одного вызова C03. Перед отправкой заказа drive проверяет, что до конца аренды
	// остаётся не меньше CallTimeout; ноль отключает проверку.
	CallTimeout time.Duration
	Retry       domain.RetryPolicy
	// ReconcileBatch — сколько строк сверщик берёт за проход.
	ReconcileBatch int
	// ReconcileInterval — период сверщика.
	ReconcileInterval time.Duration
}

// DefaultSettings — значения прототипа (application.yaml): аренда 30 с, 5 попыток, пауза 1 с, перепроверка 1 мин.
func DefaultSettings() Settings {
	return Settings{
		Lease:             30 * time.Second,
		CallTimeout:       5 * time.Second,
		Retry:             domain.RetryPolicy{MaxAttempts: 5, Backoff: time.Second, UnknownRecheck: time.Minute},
		ReconcileBatch:    100,
		ReconcileInterval: time.Second,
	}
}

// Gateway — участник саги «заказ перевозки». Безопасен для нескольких экземпляров над одной базой.
type Gateway struct {
	pool     *pgxpool.Pool
	store    Store
	carrier  Carrier
	outbox   Outbox
	clock    clock.Clock
	log      *slog.Logger
	settings Settings
}

// New собирает шлюз.
func New(pool *pgxpool.Pool, store Store, carrier Carrier, outbox Outbox, clk clock.Clock, log *slog.Logger, s Settings) *Gateway {
	return &Gateway{pool: pool, store: store, carrier: carrier, outbox: outbox, clock: clk,
		log: log.With("component", "transport-gateway"), settings: s}
}

// AcceptRequest — RequestTransport: inbox и строка REQUESTING одной транзакцией, затем Drive.
// Повтор доставки отсекает inbox, повтор отправки transfer — первичный ключ обязательства.
func (g *Gateway) AcceptRequest(ctx context.Context, msg messaging.Message, req domain.Request) error {
	first := false
	err := bspgx.WithTx(ctx, g.pool, func(ctx context.Context, tx pgx.Tx) error {
		now := g.clock.Now()
		ok, err := messaging.FirstTime(ctx, tx, msg, now)
		if err != nil || !ok {
			return err
		}
		first = true
		inserted, err := g.store.Insert(ctx, tx, req, now)
		if err != nil {
			return fmt.Errorf("запись заказа перевозки: %w", err)
		}
		if !inserted {
			return nil // повтор отправки transfer или надгробие отмены: принимать нечего
		}
		return g.outbox.Audit(ctx, tx, messaging.AuditEntry{
			Action: actionRequestAccepted, ObjectType: auditObject, ObjectID: req.ObligationID.String(),
			Result: auditc.ResultSuccess, Details: map[string]any{
				"request_id": req.RequestID.String(), "source_clinic_id": req.SourceClinicID,
				"target_clinic_id": req.TargetClinicID, "component_kind_code": req.ComponentKindCode,
				"quantity": req.Quantity, "temperature_regime": req.TemperatureRegime,
			},
		})
	})
	if err != nil {
		return err
	}
	if first {
		g.driveAfterCommit(ctx, req.ObligationID)
	}
	return nil
}

// AcceptCancel — CancelTransport. Строки нет — надгробие и сразу TransportCancelled: поздний RequestTransport
// упрётся в него. Снимать у C03 нечего (REJECTED, FAILED, уже CANCELLED) — тоже сразу TransportCancelled: шлюз
// отвечает на каждую отмену, и когда заказа не было (events.md); повтор доставки той же отмены отсекает inbox.
// Иначе флаг, заказ снимает Drive.
func (g *Gateway) AcceptCancel(ctx context.Context, msg messaging.Message, obligationID uuid.UUID) error {
	first := false
	err := bspgx.WithTx(ctx, g.pool, func(ctx context.Context, tx pgx.Tx) error {
		now := g.clock.Now()
		ok, err := messaging.FirstTime(ctx, tx, msg, now)
		if err != nil || !ok {
			return err
		}
		first = true
		before, existed, err := g.store.RequestCancel(ctx, tx, obligationID, now)
		if err != nil {
			return fmt.Errorf("отмена заказа перевозки: %w", err)
		}
		details := map[string]any{"status_before": string(before)}
		if !existed {
			details = map[string]any{"tombstone": true}
		}
		if err := g.outbox.Audit(ctx, tx, messaging.AuditEntry{
			Action: actionCancelRequested, ObjectType: auditObject, ObjectID: obligationID.String(),
			Result: auditc.ResultSuccess, Details: details,
		}); err != nil {
			return err
		}
		if !existed || domain.NothingToCancel(before) {
			if err := g.outbox.Reply(ctx, tx, domain.CancelledReply(obligationID)); err != nil {
				return err
			}
			return g.audit(ctx, tx, obligationID, domain.StatusCancelled, "")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if first {
		g.driveAfterCommit(ctx, obligationID)
	}
	return nil
}

// driveAfterCommit продвигает заказ сразу после приёма команды. Ошибка не возвращается consumer-у: команда уже
// принята (inbox), повтор доставки ничего бы не сделал; строку подберёт сверщик.
func (g *Gateway) driveAfterCommit(ctx context.Context, obligationID uuid.UUID) {
	if err := g.Drive(ctx, obligationID); err != nil {
		g.log.WarnContext(ctx, "заказ перевозки не продвинут, его подберёт сверщик",
			"obligation_id", obligationID, "err", err)
	}
}

// Drive продвигает заказ к следующему состоянию. Безопасно вызывать сколько угодно раз и из нескольких экземпляров:
// с C03 по строке говорит только владелец аренды. Недоступность C03 — не ошибка, а засчитанная попытка.
func (g *Gateway) Drive(ctx context.Context, obligationID uuid.UUID) error {
	owner := uuid.New()
	start := g.clock.Now()
	row, ok, err := g.store.Claim(ctx, obligationID, owner, start, start.Add(g.settings.Lease))
	if err != nil {
		return fmt.Errorf("аренда заказа перевозки: %w", err)
	}
	if !ok {
		return nil // делать нечего или строку держит другой экземпляр
	}
	outcome, err := g.decide(ctx, row, start)
	if err != nil {
		if ctx.Err() != nil {
			// Остановка процесса — не недоступность C03: попытку не засчитываем, аренда истечёт сама.
			return fmt.Errorf("drive прерван: %w", ctx.Err())
		}
		if errors.Is(err, errLeaseRunningOut) {
			g.log.WarnContext(ctx, "аренды не хватит на отправку заказа, ход отпущен сверщику",
				"obligation_id", obligationID)
			if err := g.store.Release(ctx, obligationID, owner, g.clock.Now()); err != nil {
				return err
			}
			return nil
		}
		if errors.Is(err, ErrCarrierUnavailable) {
			return g.recordFailure(ctx, row, owner, err)
		}
		return err
	}
	return bspgx.WithTx(ctx, g.pool, func(ctx context.Context, tx pgx.Tx) error {
		now := g.clock.Now()
		finished, cancelRequested, err := g.store.Finish(ctx, tx, obligationID, owner, row.Status, outcome.Status,
			outcome.ShipmentRef, now)
		if err != nil {
			return fmt.Errorf("запись исхода заказа перевозки: %w", err)
		}
		if !finished {
			g.log.WarnContext(ctx, "аренда заказа перевозки истекла до записи исхода, исход отброшен",
				"obligation_id", obligationID, "status", string(outcome.Status))
			return nil
		}
		if outcome.Reply != nil {
			if err := g.outbox.Reply(ctx, tx, *outcome.Reply); err != nil {
				return err
			}
		}
		// Отмена пришла, пока шли вызовы C03, а заказа так и не стало (отказ, FAILED): на отмену тоже отвечаем,
		// иначе transfer не дождётся TransportCancelled. ACCEPTED с флагом снимет следующий drive.
		if cancelRequested && outcome.Status != domain.StatusCancelled && domain.NothingToCancel(outcome.Status) {
			if err := g.outbox.Reply(ctx, tx, domain.CancelledReply(obligationID)); err != nil {
				return err
			}
		}
		return g.audit(ctx, tx, obligationID, outcome.Status, outcome.ShipmentRef)
	})
}

// errLeaseRunningOut — до конца аренды меньше таймаута вызова C03: отправлять заказ нельзя.
var errLeaseRunningOut = errors.New("аренда заказа перевозки на исходе")

// decide — все вызовы C03 здесь, вне транзакции. Сверка всегда раньше отправки. leaseStart — момент аренды.
func (g *Gateway) decide(ctx context.Context, row domain.Row, leaseStart time.Time) (domain.Outcome, error) {
	ref := domain.ClientRef(row.ObligationID)
	d := row.Begin()
	if d.Step == domain.StepReconcile {
		active, err := g.carrier.FindActive(ctx, ref)
		if err != nil {
			return domain.Outcome{}, fmt.Errorf("сверка с C03: %w", err)
		}
		d = row.AfterReconcile(active)
	}
	switch d.Step {
	case domain.StepCancelAtCarrier:
		if err := g.carrier.Cancel(ctx, ref); err != nil {
			return domain.Outcome{}, fmt.Errorf("снятие заказа у C03: %w", err)
		}
		return d.Outcome, nil
	case domain.StepSubmit:
		// Сверка могла съесть почти всю аренду (медленная C03). Если отправка не успеет до её конца, аренда
		// истечёт посреди вызова, другой экземпляр сверится, не увидит ещё не созданный заказ и отправит второй.
		// Поэтому без запаса в один таймаут не отправляем: отпускаем ход, сверщик начнёт заново со сверки.
		if g.settings.CallTimeout > 0 &&
			g.clock.Now().After(leaseStart.Add(g.settings.Lease-g.settings.CallTimeout)) {
			return domain.Outcome{}, errLeaseRunningOut
		}
		res, err := g.carrier.Submit(ctx, Order{ClientRef: ref, Request: row.Request})
		if err != nil {
			return domain.Outcome{}, fmt.Errorf("заказ у C03: %w", err)
		}
		if res.Refused {
			return domain.RefusedOutcome(row.ObligationID, res.Reason), nil
		}
		return domain.AcceptedOutcome(row.ObligationID, res.ShipmentRef), nil
	case domain.StepDone:
		return d.Outcome, nil
	default:
		return domain.Outcome{}, fmt.Errorf("неожиданный шаг drive %d", d.Step)
	}
}

// recordFailure засчитывает попытку. После MaxAttempts — UNKNOWN и TransportRejected(OUTCOME_UNKNOWN).
func (g *Gateway) recordFailure(ctx context.Context, row domain.Row, owner uuid.UUID, cause error) error {
	return bspgx.WithTx(ctx, g.pool, func(ctx context.Context, tx pgx.Tx) error {
		now := g.clock.Now()
		f := g.settings.Retry.OnCarrierUnavailable(row, now)
		ok, err := g.store.FailAttempt(ctx, tx, row.ObligationID, owner, row.Status, f.Status, cause.Error(),
			f.NextAttemptAt, now)
		if err != nil {
			return fmt.Errorf("запись неудачной попытки: %w", err)
		}
		if !ok {
			return nil // аренду уже взял другой экземпляр: его попытка важнее
		}
		g.log.WarnContext(ctx, "C03 не ответила, попытка засчитана", "obligation_id", row.ObligationID,
			"attempt", row.Attempts+1, "status", string(f.Status), "next_attempt_at", f.NextAttemptAt, "err", cause)
		if f.Reply == nil {
			return nil
		}
		if err := g.outbox.Reply(ctx, tx, *f.Reply); err != nil {
			return err
		}
		return g.audit(ctx, tx, row.ObligationID, f.Status, "")
	})
}

// ReconcileDue — один проход сверщика: всё, что просрочило следующую попытку, включая строки упавших экземпляров.
// Возвращает число подобранных строк.
func (g *Gateway) ReconcileDue(ctx context.Context) (int, error) {
	due, err := g.store.Due(ctx, g.clock.Now(), g.settings.ReconcileBatch)
	if err != nil {
		return 0, fmt.Errorf("очередь сверщика: %w", err)
	}
	for _, id := range due {
		if err := g.Drive(ctx, id); err != nil {
			if ctx.Err() != nil {
				return len(due), ctx.Err()
			}
			g.log.WarnContext(ctx, "сверка заказа не удалась, повторю", "obligation_id", id, "err", err)
		}
	}
	return len(due), nil
}

// RunReconciler — фоновый сверщик с периодом ReconcileInterval до отмены ctx. Период — расписание процесса,
// а не время предметной области, поэтому тикер настоящий; сроки строк считает Clock.
func (g *Gateway) RunReconciler(ctx context.Context) error {
	ticker := time.NewTicker(g.settings.ReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := g.ReconcileDue(ctx); err != nil && ctx.Err() == nil {
				g.log.WarnContext(ctx, "сверка с C03 упала, повторю", "err", err)
			}
		}
	}
}

// Действия аудита шлюза. Объект — обязательство O07; номеров единиц и ПДн в записях нет.
const (
	auditObject           = "obligation"
	actionRequestAccepted = "REQUEST_ACCEPTED"
	actionCancelRequested = "CANCEL_REQUESTED"
)

// audit — запись исхода в журнал той же транзакцией. Без номеров единиц и ПДн: только обязательство и заказ.
func (g *Gateway) audit(ctx context.Context, tx pgx.Tx, obligationID uuid.UUID, status domain.Status, shipmentRef string) error {
	result := auditc.ResultSuccess
	switch status {
	case domain.StatusRejected, domain.StatusUnknown, domain.StatusFailed:
		result = auditc.ResultError
	default:
		// ACCEPTED, CANCELLED: перевозка заказана или снята, как просили.
	}
	details := map[string]any{"status": string(status)}
	if shipmentRef != "" {
		details["shipment_ref"] = shipmentRef
	}
	return g.outbox.Audit(ctx, tx, messaging.AuditEntry{
		Action: "TRANSPORT_" + string(status), ObjectType: auditObject, ObjectID: obligationID.String(),
		Result: result, Details: details,
	})
}
