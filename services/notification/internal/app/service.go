package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/domain"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/letters"
)

// Действия аудита сервиса уведомлений.
const (
	ActionQueued = "NOTIFICATION_QUEUED"
	ActionSent   = "NOTIFICATION_SENT"
	ActionFailed = "NOTIFICATION_FAILED"
)

// DeliveryMetric — счётчик исходов попыток отправки (result: sent, retry, failed).
const DeliveryMetric = "notification_delivery_total"

// lastErrorLimit — предел длины last_error в символах.
const lastErrorLimit = 500

// Config — настройки отправщика.
type Config struct {
	// Interval — период опроса журнала писем.
	Interval time.Duration
	// Batch — сколько писем берётся за проход.
	Batch int
	// Lease — на сколько откладывается следующая попытка взятой записи: если процесс упадёт посреди отправки,
	// запись вернётся в работу через этот срок.
	Lease time.Duration
	// Retry — паузы между попытками.
	Retry domain.RetryPolicy
}

// DefaultConfig — настройки по умолчанию (демо).
func DefaultConfig() Config {
	return Config{Interval: 5 * time.Second, Batch: 20, Lease: 2 * time.Minute, Retry: domain.DefaultRetryPolicy()}
}

// Deps — зависимости сервиса.
type Deps struct {
	Pool      *pgxpool.Pool
	Store     Store
	Directory Directory
	Letters   *letters.Renderer
	Mailer    Mailer
	Journal   *messaging.Journal
	Clock     clock.Clock
	Log       *slog.Logger
}

// Service — сценарии уведомлений.
type Service struct {
	Deps
	cfg        Config
	deliveries metric.Int64Counter
}

// New создаёт сервис.
func New(d Deps, cfg Config) *Service {
	def := DefaultConfig()
	if cfg.Interval <= 0 {
		cfg.Interval = def.Interval
	}
	if cfg.Batch <= 0 {
		cfg.Batch = def.Batch
	}
	if cfg.Lease <= 0 {
		cfg.Lease = def.Lease
	}
	if cfg.Retry.MaxAttempts <= 0 || cfg.Retry.Base <= 0 || cfg.Retry.Max <= 0 {
		cfg.Retry = def.Retry
	}
	counter, err := otel.GetMeterProvider().Meter("github.com/AlchemistBro/blood-supply-pet/services/notification").
		Int64Counter(DeliveryMetric, metric.WithDescription("Попытки отправки писем по исходу: sent, retry, failed"))
	if err != nil {
		counter = noop.Int64Counter{}
	}
	return &Service{Deps: d, cfg: cfg, deliveries: counter}
}

// HandleMessage — обработчик transfer.events. Топик общий: события, по которым писем нет, игнорируются без
// ошибки и без записи в inbox. Битое тело нашего события — постоянная ошибка (DLT).
func (s *Service) HandleMessage(ctx context.Context, msg messaging.Message) error {
	switch msg.Type {
	case (transfer.ObligationOpened{}).MessageType():
		var e transfer.ObligationOpened
		if err := msg.Decode(&e); err != nil {
			return messaging.Permanent(err)
		}
		if e.ObligationID == uuid.Nil || e.RequestID == uuid.Nil {
			return messaging.Permanent(fmt.Errorf("%s без obligation_id или request_id", msg.Type))
		}
		return s.onObligationOpened(ctx, msg, e)
	case (transfer.EscalationRaised{}).MessageType():
		var e transfer.EscalationRaised
		if err := msg.Decode(&e); err != nil {
			return messaging.Permanent(err)
		}
		if e.ObligationID == uuid.Nil {
			return messaging.Permanent(fmt.Errorf("%s без obligation_id", msg.Type))
		}
		return s.inTx(ctx, msg, func(ctx context.Context, tx pgx.Tx) error {
			o, err := s.Store.FindObligation(ctx, tx, e.ObligationID)
			if err != nil {
				return err
			}
			return s.enqueue(ctx, tx, msg, domain.OnEscalation(e.ObligationID, e.RequestID, e.Reason, o))
		})
	case (transfer.TransferBroken{}).MessageType():
		var e transfer.TransferBroken
		if err := msg.Decode(&e); err != nil {
			return messaging.Permanent(err)
		}
		if e.ObligationID == uuid.Nil {
			return messaging.Permanent(fmt.Errorf("%s без obligation_id", msg.Type))
		}
		return s.inTx(ctx, msg, func(ctx context.Context, tx pgx.Tx) error {
			o, err := s.Store.FindObligation(ctx, tx, e.ObligationID)
			if err != nil {
				return err
			}
			return s.enqueue(ctx, tx, msg, domain.OnTransferBroken(e.ObligationID, e.RequestID, e.Reason, o))
		})
	default:
		return nil
	}
}

func (s *Service) onObligationOpened(ctx context.Context, msg messaging.Message, e transfer.ObligationOpened) error {
	o := domain.Obligation{
		ObligationID: e.ObligationID, RequestID: e.RequestID, Origin: e.Origin,
		SourceClinicID: e.SourceClinicID, TargetClinicID: e.TargetClinicID,
		ComponentKind: e.ComponentKind, RequestedQty: e.RequestedQty, Deadline: e.Deadline,
		// Срочности в ObligationOpened v1 нет (см. domain.OnObligationOpened).
	}
	openedAt := msg.OccurredAt
	if openedAt.IsZero() {
		openedAt = s.Clock.Now()
	}
	return s.inTx(ctx, msg, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.Store.SaveObligation(ctx, tx, o, openedAt); err != nil {
			return err
		}
		d, ok := domain.OnObligationOpened(o)
		if !ok {
			return nil
		}
		return s.enqueue(ctx, tx, msg, d)
	})
}

// inTx — транзакция обработчика с inbox: повтор доставки отбрасывается.
func (s *Service) inTx(ctx context.Context, msg messaging.Message, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return bspgx.WithTx(ctx, s.Pool, func(ctx context.Context, tx pgx.Tx) error {
		first, err := messaging.FirstTime(ctx, tx, msg, s.Clock.Now())
		if err != nil {
			return err
		}
		if !first {
			return nil
		}
		return fn(ctx, tx)
	})
}

// enqueue записывает письмо в журнал и аудит той же транзакцией. Сетевых вызовов здесь нет: отправка — позже.
func (s *Service) enqueue(ctx context.Context, tx pgx.Tx, msg messaging.Message, d domain.Draft) error {
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("uuid v7: %w", err)
	}
	email, _ := s.Directory.Address(d.To)
	n := domain.NewNotification(id, msg.ID, d, email, s.Letters.Subject(d.Kind, d.Facts), s.Clock.Now())
	if err := s.Store.AddNotification(ctx, tx, n); err != nil {
		return err
	}
	action, result := ActionQueued, auditc.ResultSuccess
	if n.Status == domain.StatusFailed {
		action, result = ActionFailed, auditc.ResultError
		s.Log.WarnContext(ctx, "адресат уведомления не настроен", "kind", string(n.Kind), "recipient_role", n.To.Role,
			"notification_id", n.ID.String())
	}
	return s.audit(ctx, tx, n, action, result)
}

func (s *Service) audit(ctx context.Context, tx pgx.Tx, n domain.Notification, action, result string) error {
	return s.Journal.Audit(ctx, tx, messaging.AuditEntry{
		Action: action, ObjectType: "obligation", ObjectID: n.Facts.ObligationID.String(), Result: result,
		ClinicID: n.To.ClinicID,
		Details:  map[string]any{"notification_id": n.ID.String(), "kind": string(n.Kind), "recipient_role": n.To.Role},
	})
}

// RunSender — фоновый отправщик: проходы по журналу писем до отмены контекста.
func (s *Service) RunSender(ctx context.Context) error {
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	for {
		if _, err := s.SendDue(ctx); err != nil && ctx.Err() == nil {
			s.Log.ErrorContext(ctx, "проход отправщика писем", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// SendDue — один проход: берёт созревшие письма и отправляет их вне транзакций. Возвращает число отправленных.
func (s *Service) SendDue(ctx context.Context) (int, error) {
	now := s.Clock.Now()
	due, err := s.Store.ClaimDue(ctx, now, now.Add(s.cfg.Lease), s.cfg.Batch)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, n := range due {
		if ctx.Err() != nil {
			// Взятые записи вернутся в работу по истечении аренды.
			return sent, ctx.Err()
		}
		ok, err := s.deliver(ctx, n)
		if err != nil {
			return sent, err
		}
		if ok {
			sent++
		}
	}
	return sent, nil
}

func (s *Service) deliver(ctx context.Context, n domain.Notification) (bool, error) {
	l, err := s.Letters.Render(n.Kind, n.Facts)
	var sendErr error
	if err != nil {
		sendErr = fmt.Errorf("%w: %w", ErrRejected, err)
	} else {
		sendErr = s.Mailer.Send(ctx, Mail{ID: n.ID, To: n.Email, Subject: n.Subject, Text: l.Text, HTML: l.HTML})
	}
	now := s.Clock.Now()
	if sendErr == nil {
		err := bspgx.WithTx(ctx, s.Pool, func(ctx context.Context, tx pgx.Tx) error {
			ok, err := s.Store.MarkSent(ctx, tx, n.ID, now)
			if err != nil || !ok {
				return err
			}
			return s.audit(ctx, tx, n, ActionSent, auditc.ResultSuccess)
		})
		if err != nil {
			return false, err
		}
		s.count(ctx, n.Kind, "sent")
		return true, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	lastErr := truncate(sendErr.Error(), lastErrorLimit)
	status, next := s.cfg.Retry.AfterFailure(n.Attempts, errors.Is(sendErr, ErrRejected), now)
	// В журнал процесса — без адреса и текста письма.
	s.Log.WarnContext(ctx, "письмо не отправлено", "kind", string(n.Kind), "notification_id", n.ID.String(),
		"attempt", n.Attempts, "status", string(status), "err", lastErr)
	err = bspgx.WithTx(ctx, s.Pool, func(ctx context.Context, tx pgx.Tx) error {
		if status == domain.StatusFailed {
			ok, err := s.Store.MarkFailed(ctx, tx, n.ID, lastErr)
			if err != nil || !ok {
				return err
			}
			return s.audit(ctx, tx, n, ActionFailed, auditc.ResultError)
		}
		_, err := s.Store.MarkRetry(ctx, tx, n.ID, next, lastErr)
		return err
	})
	if err != nil {
		return false, err
	}
	if status == domain.StatusFailed {
		s.count(ctx, n.Kind, "failed")
	} else {
		s.count(ctx, n.Kind, "retry")
	}
	return false, nil
}

func (s *Service) count(ctx context.Context, kind domain.Kind, result string) {
	s.deliveries.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", string(kind)), attribute.String("result", result)))
}

// List — журнал писем для ИТ-службы, новые сначала.
func (s *Service) List(ctx context.Context, f ListFilter) ([]domain.Notification, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	return s.Store.List(ctx, f)
}

func truncate(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)
	return string(r[:limit])
}
