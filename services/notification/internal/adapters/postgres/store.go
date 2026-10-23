// Package postgres — хранилище журнала писем и проекции заданий на sqlc-запросах.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/adapters/postgres/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/domain"
)

// Store реализует app.Store.
type Store struct {
	pool *pgxpool.Pool
}

// New создаёт хранилище.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

var _ app.Store = (*Store)(nil)

// SaveObligation сохраняет сведения о задании; повтор строку не перезаписывает.
func (s *Store) SaveObligation(ctx context.Context, tx pgx.Tx, o domain.Obligation, openedAt time.Time) error {
	qty, err := int32Of(o.RequestedQty)
	if err != nil {
		return err
	}
	if err := gen.New(tx).InsertObligation(ctx, gen.InsertObligationParams{
		ObligationID: o.ObligationID, RequestID: o.RequestID, Origin: o.Origin,
		SourceClinicID: o.SourceClinicID, TargetClinicID: o.TargetClinicID, ComponentKindCode: o.ComponentKind,
		RequestedQty: qty, UrgencyCode: nullable(o.Urgency), Deadline: o.Deadline, OpenedAt: openedAt,
	}); err != nil {
		return fmt.Errorf("проекция задания: %w", err)
	}
	return nil
}

// FindObligation возвращает сведения о задании или nil.
func (s *Store) FindObligation(ctx context.Context, tx pgx.Tx, obligationID uuid.UUID) (*domain.Obligation, error) {
	row, err := gen.New(tx).GetObligation(ctx, obligationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("проекция задания: %w", err)
	}
	return &domain.Obligation{
		ObligationID: row.ObligationID, RequestID: row.RequestID, Origin: row.Origin,
		SourceClinicID: row.SourceClinicID, TargetClinicID: row.TargetClinicID, ComponentKind: row.ComponentKindCode,
		RequestedQty: int(row.RequestedQty), Urgency: deref(row.UrgencyCode), Deadline: row.Deadline,
	}, nil
}

// AddNotification добавляет запись журнала писем.
func (s *Store) AddNotification(ctx context.Context, tx pgx.Tx, n domain.Notification) error {
	var qty *int32
	if n.Facts.Quantity != nil {
		q, err := int32Of(*n.Facts.Quantity)
		if err != nil {
			return err
		}
		qty = &q
	}
	attempts, err := int32Of(n.Attempts)
	if err != nil {
		return err
	}
	requestID := &n.Facts.RequestID
	if n.Facts.RequestID == uuid.Nil {
		requestID = nil
	}
	if err := gen.New(tx).InsertNotification(ctx, gen.InsertNotificationParams{
		NotificationID: n.ID, Kind: string(n.Kind), ObligationID: n.Facts.ObligationID, RequestID: requestID,
		SourceMessageID: n.SourceMessageID, RecipientRole: n.To.Role, RecipientClinicID: n.To.ClinicID,
		RecipientEmail: nullable(n.Email), Subject: n.Subject,
		SourceClinicID: n.Facts.SourceClinicID, TargetClinicID: n.Facts.TargetClinicID,
		ComponentKindCode: nullable(n.Facts.ComponentKind), Quantity: qty, UrgencyCode: nullable(n.Facts.Urgency),
		Deadline: n.Facts.Deadline, Reason: nullable(n.Facts.Reason),
		Status: string(n.Status), Attempts: attempts, NextAttemptAt: n.NextAttemptAt, LastError: nullable(n.LastError),
		CreatedAt: n.CreatedAt, SentAt: n.SentAt,
	}); err != nil {
		return fmt.Errorf("запись в журнал писем: %w", err)
	}
	return nil
}

// ClaimDue берёт созревшие записи одним запросом (FOR UPDATE SKIP LOCKED внутри UPDATE).
func (s *Store) ClaimDue(ctx context.Context, now, leaseUntil time.Time, limit int) ([]domain.Notification, error) {
	batch, err := int32Of(limit)
	if err != nil {
		return nil, err
	}
	rows, err := gen.New(s.pool).ClaimDue(ctx, gen.ClaimDueParams{LeaseUntil: leaseUntil, DueAt: now, Batch: batch})
	if err != nil {
		return nil, fmt.Errorf("выбор писем к отправке: %w", err)
	}
	return toDomainAll(rows), nil
}

// MarkSent — PENDING → SENT.
func (s *Store) MarkSent(ctx context.Context, tx pgx.Tx, id uuid.UUID, at time.Time) (bool, error) {
	n, err := gen.New(tx).MarkSent(ctx, gen.MarkSentParams{SentAt: &at, NotificationID: id})
	if err != nil {
		return false, fmt.Errorf("отметка об отправке: %w", err)
	}
	return n == 1, nil
}

// MarkRetry — запись остаётся PENDING до следующей попытки.
func (s *Store) MarkRetry(ctx context.Context, tx pgx.Tx, id uuid.UUID, next time.Time, lastErr string) (bool, error) {
	n, err := gen.New(tx).MarkRetry(ctx, gen.MarkRetryParams{NextAttemptAt: next, LastError: nullable(lastErr), NotificationID: id})
	if err != nil {
		return false, fmt.Errorf("отметка о повторе: %w", err)
	}
	return n == 1, nil
}

// MarkFailed — PENDING → FAILED.
func (s *Store) MarkFailed(ctx context.Context, tx pgx.Tx, id uuid.UUID, lastErr string) (bool, error) {
	n, err := gen.New(tx).MarkFailed(ctx, gen.MarkFailedParams{LastError: nullable(lastErr), NotificationID: id})
	if err != nil {
		return false, fmt.Errorf("отметка об отказе: %w", err)
	}
	return n == 1, nil
}

// List — записи по фильтру, новые сначала.
func (s *Store) List(ctx context.Context, f app.ListFilter) ([]domain.Notification, error) {
	limit, err := int32Of(f.Limit)
	if err != nil {
		return nil, err
	}
	var status *string
	if f.Status != nil {
		st := string(*f.Status)
		status = &st
	}
	rows, err := gen.New(s.pool).ListNotifications(ctx, gen.ListNotificationsParams{Status: status, PageLimit: limit})
	if err != nil {
		return nil, fmt.Errorf("журнал писем: %w", err)
	}
	return toDomainAll(rows), nil
}

func toDomainAll(rows []gen.Notification) []domain.Notification {
	out := make([]domain.Notification, 0, len(rows))
	for _, r := range rows {
		out = append(out, toDomain(r))
	}
	return out
}

func toDomain(r gen.Notification) domain.Notification {
	f := domain.Facts{
		ObligationID: r.ObligationID, SourceClinicID: r.SourceClinicID, TargetClinicID: r.TargetClinicID,
		ComponentKind: deref(r.ComponentKindCode), Urgency: deref(r.UrgencyCode), Deadline: r.Deadline, Reason: deref(r.Reason),
	}
	if r.RequestID != nil {
		f.RequestID = *r.RequestID
	}
	if r.Quantity != nil {
		q := int(*r.Quantity)
		f.Quantity = &q
	}
	return domain.Notification{
		ID: r.NotificationID, Kind: domain.Kind(r.Kind), SourceMessageID: r.SourceMessageID,
		To: domain.Recipient{Role: r.RecipientRole, ClinicID: r.RecipientClinicID}, Email: deref(r.RecipientEmail),
		Subject: r.Subject, Facts: f, Status: domain.Status(r.Status), Attempts: int(r.Attempts),
		NextAttemptAt: r.NextAttemptAt, LastError: deref(r.LastError), CreatedAt: r.CreatedAt, SentAt: r.SentAt,
	}
}

func int32Of(v int) (int32, error) {
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, fmt.Errorf("значение %d вне диапазона integer", v)
	}
	return int32(v), nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
