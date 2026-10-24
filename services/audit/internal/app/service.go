// Package app — сценарии сервиса аудита: приём записей из audit.events и от web, выдача журнала,
// проверка цепочки. Транзакции и идемпотентность (inbox) здесь; хеширование — в domain.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/services/audit/internal/domain"
)

// Store — порт хранилища журнала.
type Store interface {
	// Append блокирует голову цепочки, вызывает link с её состоянием и сохраняет запись и новую голову.
	Append(ctx context.Context, tx pgx.Tx, link func(headSeq int64, headHash []byte) (domain.Record, error)) (domain.Record, error)
	// List возвращает страницу записей по фильтру, упорядоченную по seq.
	List(ctx context.Context, f Filter) ([]domain.Record, error)
	// Walk обходит все записи по порядку seq пачками.
	Walk(ctx context.Context, fromSeq int64, batch int, fn func(domain.Record) error) error
}

// Filter — фильтры списка.
type Filter struct {
	Module, Action, ObjectType, ObjectID, UserID *string
	From, To                                     *time.Time
	AfterSeq                                     int64
	Limit                                        int
}

// Page — страница записей с курсором.
type Page struct {
	Items        []domain.Record
	NextAfterSeq *int64
}

// VerifyReport — результат проверки цепочки.
type VerifyReport struct {
	OK       bool
	Checked  int64
	HeadHash []byte
	Broken   *domain.BreakReport
}

// ErrInvalid — входные данные не проходят проверку.
var ErrInvalid = errors.New("некорректная запись аудита")

// Service — сценарии аудита.
type Service struct {
	pool       *pgxpool.Pool
	store      Store
	clock      clock.Clock
	log        *slog.Logger
	security   SecurityConfig
	journal    *messaging.Journal
	verifyOK   atomic.Int64
	verifyLast atomic.Int64
}

// New создаёт сервис.
func New(pool *pgxpool.Pool, store Store, clk clock.Clock, log *slog.Logger) *Service {
	return &Service{pool: pool, store: store, clock: clk, log: log, security: DefaultSecurityConfig(), journal: messaging.NewJournal("audit", auditc.ModuleAudit, clk)}
}

// HandleMessage — обработчик audit.events: inbox + добавление в цепочку одной транзакцией.
// Повтор доставки отбрасывается inbox; неизвестный тип — постоянная ошибка (DLT).
func (s *Service) HandleMessage(ctx context.Context, msg messaging.Message) error {
	if msg.Type != (auditc.AuditRecorded{}).MessageType() {
		return messaging.Permanent(fmt.Errorf("неожиданный тип в audit.events: %s", msg.Type))
	}
	var rec auditc.AuditRecorded
	if err := msg.Decode(&rec); err != nil {
		return messaging.Permanent(err)
	}
	if err := validate(rec); err != nil {
		return messaging.Permanent(err)
	}
	return bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		first, err := messaging.FirstTime(ctx, tx, msg, s.clock.Now())
		if err != nil {
			return err
		}
		if !first {
			return nil
		}
		_, err = s.append(ctx, tx, rec, msg.Producer, msg.ID)
		return err
	})
}

// AppendDirect добавляет запись, пришедшую по HTTP (web не имеет своей базы и outbox, ADR-0017).
func (s *Service) AppendDirect(ctx context.Context, rec auditc.AuditRecorded, producer string) (domain.Record, error) {
	if err := validate(rec); err != nil {
		return domain.Record{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return domain.Record{}, err
	}
	var out domain.Record
	err = bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		out, err = s.append(ctx, tx, rec, producer, id)
		return err
	})
	return out, err
}

func (s *Service) append(ctx context.Context, tx pgx.Tx, rec auditc.AuditRecorded, producer string, sourceID uuid.UUID) (domain.Record, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return domain.Record{}, err
	}
	base := domain.Record{
		ID: id, OccurredAt: rec.OccurredAt.UTC(), ReceivedAt: s.clock.Now(), UserID: rec.UserID, Role: rec.Role,
		ClinicID: rec.ClinicID, Module: rec.Module, Action: rec.Action, ObjectType: rec.ObjectType, ObjectID: rec.ObjectID,
		Result: rec.Result, Details: rec.Details, CorrelationID: rec.CorrelationID, Producer: producer, SourceMessageID: sourceID,
	}
	stored, err := s.store.Append(ctx, tx, func(headSeq int64, headHash []byte) (domain.Record, error) {
		return domain.Link(headSeq, headHash, base)
	})
	if err != nil {
		// Нарушение уникальности здесь означало бы разрыв цепочки (seq) или повтор source_message_id.
		// Транзакция уже прервана, поэтому ошибка передаётся наверх: дубли Kafka отсекает inbox,
		// а у записей, пришедших по HTTP, source_message_id всегда новый.
		return domain.Record{}, err
	}
	if err := s.detect(ctx, tx, stored); err != nil {
		return domain.Record{}, err
	}
	return stored, nil
}

func validate(rec auditc.AuditRecorded) error {
	switch {
	case rec.OccurredAt.IsZero():
		return fmt.Errorf("%w: нет occurred_at", ErrInvalid)
	case rec.Module == "" || rec.Action == "" || rec.ObjectType == "" || rec.ObjectID == "":
		return fmt.Errorf("%w: module, action, object_type, object_id обязательны", ErrInvalid)
	case rec.Result != auditc.ResultSuccess && rec.Result != auditc.ResultDenied && rec.Result != auditc.ResultError:
		return fmt.Errorf("%w: недопустимый result %q", ErrInvalid, rec.Result)
	}
	for k := range rec.Details {
		switch k {
		case "patient_case_no", "full_name", "snils", "phenotype", "password":
			return fmt.Errorf("%w: details содержит запрещённое поле %s", ErrInvalid, k)
		}
	}
	return nil
}

// List отдаёт страницу журнала.
func (s *Service) List(ctx context.Context, f Filter) (Page, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	items, err := s.store.List(ctx, f)
	if err != nil {
		return Page{}, err
	}
	page := Page{Items: items}
	if len(items) == f.Limit {
		last := items[len(items)-1].Seq
		page.NextAfterSeq = &last
	}
	return page, nil
}

// Verify пересчитывает цепочку от первой записи и сообщает о первом разрыве.
func (s *Service) Verify(ctx context.Context) (VerifyReport, error) {
	v := domain.NewVerifier()
	var broken *domain.BreakReport
	err := s.store.Walk(ctx, 0, 1000, func(r domain.Record) error {
		br, err := v.Next(r)
		if err != nil {
			return err
		}
		if br != nil {
			broken = br
			return errStopWalk
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		var chainBreak *domain.ChainBreakError
		if !errors.As(err, &chainBreak) {
			return VerifyReport{}, err
		}
		broken = &chainBreak.Report
	}
	if broken != nil {
		s.verifyOK.Store(0)
		if err := s.reportBreak(ctx, broken); err != nil {
			return VerifyReport{}, err
		}
	}
	if broken == nil {
		s.verifyOK.Store(1)
	}
	s.verifyLast.Store(s.clock.Now().Unix())
	return VerifyReport{OK: broken == nil, Checked: v.Checked(), HeadHash: v.HeadHash(), Broken: broken}, nil
}

var errStopWalk = errors.New("stop")
