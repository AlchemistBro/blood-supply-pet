// Package app — сценарии сервиса уведомлений: приём событий transfer.events (транзакция, inbox, запись в журнал писем,
// аудит), отправка созревших писем вне транзакции с повторами, выдача журнала ИТ-службе.
//
// Сбой почты не меняет задание (FR044): сервис ничего не пишет в transfer, а письмо, которое не ушло, остаётся
// PENDING и ждёт следующей попытки.
package app

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/domain"
)

// Store — порт хранилища журнала писем и проекции заданий.
type Store interface {
	// SaveObligation сохраняет сведения о задании из ObligationOpened; повтор не перезаписывает строку.
	SaveObligation(ctx context.Context, tx pgx.Tx, o domain.Obligation, openedAt time.Time) error
	// FindObligation возвращает сведения о задании или nil, если ObligationOpened не приходил.
	FindObligation(ctx context.Context, tx pgx.Tx, obligationID uuid.UUID) (*domain.Obligation, error)
	// AddNotification добавляет запись журнала писем.
	AddNotification(ctx context.Context, tx pgx.Tx, n domain.Notification) error
	// ClaimDue берёт до limit созревших записей PENDING, увеличивает счётчик попыток и откладывает следующую попытку
	// до leaseUntil, чтобы запись не взял параллельный отправщик.
	ClaimDue(ctx context.Context, now, leaseUntil time.Time, limit int) ([]domain.Notification, error)
	// MarkSent, MarkRetry, MarkFailed — переходы из PENDING (CAS); false — запись уже не PENDING.
	MarkSent(ctx context.Context, tx pgx.Tx, id uuid.UUID, at time.Time) (bool, error)
	MarkRetry(ctx context.Context, tx pgx.Tx, id uuid.UUID, next time.Time, lastErr string) (bool, error)
	MarkFailed(ctx context.Context, tx pgx.Tx, id uuid.UUID, lastErr string) (bool, error)
	// Get возвращает запись по идентификатору.
	Get(ctx context.Context, id uuid.UUID) (domain.Notification, error)
	// List — записи по фильтру, новые сначала.
	List(ctx context.Context, f ListFilter) ([]domain.Notification, error)
}

// ListFilter — фильтр журнала писем.
type ListFilter struct {
	Status *domain.Status
	Limit  int
}

// Directory — справочник адресатов (ADR-0021).
type Directory interface {
	Address(r domain.Recipient) (string, bool)
}

// Mail — письмо к отправке. ID — идентификатор записи notification, он же Message-ID письма.
type Mail struct {
	ID      uuid.UUID
	To      string
	Subject string
	Text    string
	HTML    string
}

// Mailer — порт почтового сервиса C04 (ИНТ-04).
type Mailer interface {
	Send(ctx context.Context, m Mail) error
}

// ErrRejected — сервер отклонил письмо окончательно (код 5xx): повтор бессмыслен. Остальные ошибки отправки
// (нет соединения, таймаут, код 4xx) — временные.
var ErrRejected = errors.New("почтовый сервер отклонил письмо")

// ErrNotFound — записи нет.
var ErrNotFound = errors.New("запись не найдена")
