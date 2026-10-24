package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

// Коды действий аудита transfer. Details не содержат фенотипа и спецификации замены.
const (
	ActionOpened                = "OPENED"
	ActionConfirmed             = "CONFIRMED"
	ActionRejected              = "REJECTED"
	ActionExpired               = "EXPIRED"
	ActionCancelled             = "CANCELLED"
	ActionBroken                = "BROKEN"
	ActionUnitReplaced          = "UNIT_REPLACED"
	ActionTransportRequested    = "TRANSPORT_REQUESTED"
	ActionTransportReplyIgnored = "TRANSPORT_REPLY_IGNORED"
	ActionTransportCancelled    = "TRANSPORT_CANCELLED"
	ActionTransportFailed       = "TRANSPORT_FAILED"
	// ActionEscalated — задание передано дежурному без смены статуса (экстренное, АИСТ-М не ответила).
	ActionEscalated = "ESCALATED"
)

// Участники, от имени которых действует сам сервис.
const (
	actorSystem    = "system"
	actorLogistics = "logistics-gateway"
)

const objectObligation = "obligation"

// Service — сага подтверждения передачи, оркестрация в transfer (ADR-0004).
//
// Порядок блокировок везде одинаковый: строка source_stock, затем строка obligation. Обработчик ответов
// логистики и таймер блокируют только obligation. Сетевых вызовов внутри транзакций нет: статус единиц
// в АИСТ-М проверяется до транзакции.
type Service struct {
	pool    *pgxpool.Pool
	store   Store
	checker UnitStatusChecker
	journal *messaging.Journal
	clock   clock.Clock
	log     *slog.Logger
	cfg     Config
	metrics metrics
}

// New собирает сервис.
func New(pool *pgxpool.Pool, store Store, checker UnitStatusChecker, journal *messaging.Journal, clk clock.Clock,
	log *slog.Logger, cfg Config) *Service {
	if cfg.AllowCopyConfirmEmergency {
		log.Warn("BS_ALLOW_COPY_CONFIRM_EMERGENCY=true пока не действует: экстренное задание без ответа АИСТ-М не подтверждается")
	}
	if cfg.ExpiryInterval <= 0 {
		cfg.ExpiryInterval = 5 * time.Second
	}
	if cfg.ExpiryBatch <= 0 {
		cfg.ExpiryBatch = 100
	}
	return &Service{pool: pool, store: store, checker: checker, journal: journal, clock: clk, log: log, cfg: cfg,
		metrics: newMetrics()}
}

// inTx выполняет fn в транзакции. Если задано входящее сообщение, сначала отмечает его в inbox:
// повтор доставки Kafka ничего не меняет (ADR-0003).
func (s *Service) inTx(ctx context.Context, msg *messaging.Message, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		if msg != nil {
			first, err := messaging.FirstTime(ctx, tx, *msg, s.clock.Now())
			if err != nil {
				return err
			}
			if !first {
				return nil
			}
		}
		return fn(ctx, tx)
	})
}

func (s *Service) event(ctx context.Context, tx pgx.Tx, obligationID uuid.UUID, p contracts.Payload) error {
	if _, err := s.journal.Publish(ctx, tx, contracts.TopicTransferEvents, obligationID.String(), p); err != nil {
		return fmt.Errorf("событие %s: %w", p.MessageType(), err)
	}
	return nil
}

func (s *Service) command(ctx context.Context, tx pgx.Tx, obligationID uuid.UUID, p contracts.Payload) error {
	if _, err := s.journal.Publish(ctx, tx, contracts.TopicLogisticsCommands, obligationID.String(), p); err != nil {
		return fmt.Errorf("команда %s: %w", p.MessageType(), err)
	}
	return nil
}

func (s *Service) audit(ctx context.Context, tx pgx.Tx, actor, action string, o domain.Obligation, details map[string]any) error {
	clinic := o.SourceClinicID
	if err := s.journal.Audit(ctx, tx, messaging.AuditEntry{
		UserID: actor, ClinicID: &clinic, Action: action, ObjectType: objectObligation, ObjectID: o.ID.String(),
		Result: auditc.ResultSuccess, Details: details,
	}); err != nil {
		return fmt.Errorf("аудит %s: %w", action, err)
	}
	return nil
}

// Open открывает задание источнику (O07 PENDING). Идемпотентно по choice_id: повтор выбора врача
// или предложения плана не создаёт второе задание. msg — входящая команда (nil при прямом вызове).
func (s *Service) Open(ctx context.Context, msg *messaging.Message, n domain.NewObligation) (uuid.UUID, error) {
	if err := n.Validate(); err != nil {
		return uuid.Nil, deliveryErr(msg, err)
	}
	err := s.inTx(ctx, msg, func(ctx context.Context, tx pgx.Tx) error {
		_, _, err := s.openTx(ctx, tx, n, actorSystem)
		return err
	})
	if err != nil {
		return uuid.Nil, deliveryErr(msg, err)
	}
	return s.store.ObligationIDByChoice(ctx, s.pool, n.ChoiceID)
}

// openTx вставляет задание и пишет ObligationOpened и аудит, только если вставка была первой.
func (s *Service) openTx(ctx context.Context, tx pgx.Tx, n domain.NewObligation, actor string) (uuid.UUID, bool, error) {
	now := s.clock.Now()
	// Клиники проверяются по справочнику до вставки: неизвестный запасной источник иначе всплыл бы только
	// при эскалации нарушением FK.
	clinics := append([]int64{n.SourceClinicID, n.TargetClinicID}, n.FallbackSources...)
	unknown, err := s.store.UnknownClinics(ctx, tx, clinics)
	if err != nil {
		return uuid.Nil, false, err
	}
	if len(unknown) > 0 {
		return uuid.Nil, false, fmt.Errorf("%w: неизвестные клиники %v", domain.ErrInvalidObligation, unknown)
	}
	window := n.ConfirmWindow
	if window == 0 {
		w, err := s.store.UrgencyWindow(ctx, tx, n.UrgencyCode)
		if err != nil {
			return uuid.Nil, false, err
		}
		window = w
	}
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("uuid v7: %w", err)
	}
	o := domain.Obligation{
		ID: id, ChoiceID: n.ChoiceID, RequestID: n.RequestID, Origin: n.Origin, SourceClinicID: n.SourceClinicID,
		TargetClinicID: n.TargetClinicID, ComponentKind: n.ComponentKind, BloodGroup: n.Spec.BloodGroup,
		RequestedQty: n.RequestedQty, Status: domain.StatusPending, UrgencyCode: n.UrgencyCode,
		Deadline: domain.Deadline(now, window), ConfirmWindow: window, FallbackSources: n.FallbackSources,
		Spec: n.Spec, CreatedAt: now, UpdatedAt: now,
	}
	inserted, err := s.store.InsertObligation(ctx, tx, o)
	if err != nil {
		return uuid.Nil, false, err
	}
	if !inserted {
		existing, err := s.store.ObligationIDByChoice(ctx, tx, n.ChoiceID)
		return existing, false, err
	}
	if err := s.event(ctx, tx, o.ID, ObligationOpened{
		ObligationID: o.ID, RequestID: o.RequestID, Origin: string(o.Origin), SourceClinicID: o.SourceClinicID,
		TargetClinicID: o.TargetClinicID, ComponentKind: o.ComponentKind, RequestedQty: o.RequestedQty, Deadline: o.Deadline,
	}); err != nil {
		return uuid.Nil, false, err
	}
	if err := s.audit(ctx, tx, actor, ActionOpened, o, map[string]any{
		"requested_qty": o.RequestedQty, "target_clinic_id": o.TargetClinicID, "origin": string(o.Origin),
	}); err != nil {
		return uuid.Nil, false, err
	}
	return o.ID, true, nil
}

// Find возвращает обязательство с действующими закреплениями.
func (s *Service) Find(ctx context.Context, id uuid.UUID) (ObligationView, error) {
	o, err := s.store.Obligation(ctx, s.pool, id)
	if err != nil {
		return ObligationView{}, err
	}
	return s.view(ctx, o)
}

// ByRequest — все обязательства заявки (или плана), включая открытые эскалацией.
func (s *Service) ByRequest(ctx context.Context, requestID uuid.UUID) ([]ObligationView, error) {
	list, err := s.store.ObligationsByRequest(ctx, s.pool, requestID)
	if err != nil {
		return nil, err
	}
	return s.views(ctx, list)
}

func (s *Service) views(ctx context.Context, list []domain.Obligation) ([]ObligationView, error) {
	out := make([]ObligationView, 0, len(list))
	for _, o := range list {
		v, err := s.view(ctx, o)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// BySourceLimit — сколько заданий источника отдаёт список (экран оператора, новые первыми).
const BySourceLimit = 200

// BySource — задания клиники-источника; status nil — любой.
func (s *Service) BySource(ctx context.Context, sourceClinicID int64, status *domain.Status) ([]ObligationView, error) {
	list, err := s.store.ObligationsBySource(ctx, s.pool, sourceClinicID, status, BySourceLimit)
	if err != nil {
		return nil, err
	}
	return s.views(ctx, list)
}

// AllocationHistory — все закрепления единицы, включая снятые: куда уходила единица (прослеживаемость, ПП РФ № 641, п. 17–18, 54).
func (s *Service) AllocationHistory(ctx context.Context, unitID uuid.UUID) ([]domain.Allocation, error) {
	return s.store.AllocationHistory(ctx, s.pool, unitID)
}

func (s *Service) view(ctx context.Context, o domain.Obligation) (ObligationView, error) {
	units, err := s.store.ConfirmedUnits(ctx, s.pool, o.ID)
	if err != nil {
		return ObligationView{}, err
	}
	return ObligationView{Obligation: o, Units: units}, nil
}

// permanentIfInvalid помечает ошибки, которые повтор не исправит: consumer отправит сообщение в DLT.
// Нарушения внешнего ключа и CHECK (неизвестная клиника или вид, отрицательный остаток) тоже постоянны:
// без этого сообщение повторялось бы бесконечно и остановило бы раздел Kafka.
func permanentIfInvalid(err error) error {
	if errors.Is(err, ErrInvalidEvent) || errors.Is(err, ErrNotFound) || errors.Is(err, domain.ErrInvalidObligation) ||
		isConstraintViolation(err) {
		return messaging.Permanent(err)
	}
	return err
}

// SQLSTATE нарушений, которые повтор не исправит.
const (
	sqlStateForeignKey = "23503"
	sqlStateCheck      = "23514"
)

func isConstraintViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == sqlStateForeignKey || pgErr.Code == sqlStateCheck)
}
