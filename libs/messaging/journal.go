package messaging

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/correlation"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations — платформенные миграции (outbox, inbox, audit_local), версии 00001–00099.
// Сервис объединяет их со своими через pgx.Migrate.
var Migrations fs.FS = mustSub(migrationFiles, "migrations")

func mustSub(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

// Journal пишет события, команды и аудит в outbox в транзакции бизнес-операции.
// Подтверждение без команды на перевозку (или наоборот) невозможно именно из-за этого.
type Journal struct {
	producer string
	module   string
	clock    clock.Clock
}

// NewJournal создаёт журнал для сервиса producer (имя попадает в заголовок).
func NewJournal(producer, module string, c clock.Clock) *Journal {
	return &Journal{producer: producer, module: module, clock: c}
}

// Publish записывает сообщение в outbox. Ключ — идентификатор агрегата (порядок в партиции).
func (j *Journal) Publish(ctx context.Context, tx pgx.Tx, topic, key string, payload contracts.Payload) (Message, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return Message{}, fmt.Errorf("сериализация %s: %w", payload.MessageType(), err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Message{}, fmt.Errorf("uuid v7: %w", err)
	}
	ids := correlation.From(ctx)
	msg := Message{
		ID:            id,
		Type:          payload.MessageType(),
		SchemaVersion: payload.SchemaVersion(),
		Topic:         topic,
		Key:           key,
		Payload:       body,
		CorrelationID: ids.Correlation,
		CausationID:   ids.Causation,
		Producer:      j.producer,
		OccurredAt:    j.clock.Now(),
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO outbox (message_id, topic, key, message_type, schema_version, payload,
		                    correlation_id, causation_id, producer, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		msg.ID, msg.Topic, msg.Key, msg.Type, msg.SchemaVersion, msg.Payload,
		nullable(msg.CorrelationID), nullable(msg.CausationID), msg.Producer, msg.OccurredAt)
	if err != nil {
		return Message{}, fmt.Errorf("запись в outbox: %w", err)
	}
	return msg, nil
}

// AuditEntry — что записать в аудит; время и модуль журнал проставит сам.
type AuditEntry struct {
	UserID     string
	Role       string
	ClinicID   *int64
	Action     string
	ObjectType string
	ObjectID   string
	Result     string
	Details    map[string]any
}

// Audit пишет запись локально (audit_local, NFR008) и отправляет AuditRecorded в audit.events
// той же транзакцией. Ключ — имя сервиса: порядок записей одного сервиса сохраняется.
func (j *Journal) Audit(ctx context.Context, tx pgx.Tx, e AuditEntry) error {
	ids := correlation.From(ctx)
	rec := audit.AuditRecorded{
		OccurredAt:    j.clock.Now(),
		UserID:        e.UserID,
		Role:          e.Role,
		ClinicID:      e.ClinicID,
		Module:        j.module,
		Action:        e.Action,
		ObjectType:    e.ObjectType,
		ObjectID:      e.ObjectID,
		Result:        e.Result,
		Details:       e.Details,
		CorrelationID: ids.Correlation,
	}
	details, err := json.Marshal(rec.Details)
	if err != nil {
		return fmt.Errorf("сериализация details: %w", err)
	}
	if rec.Details == nil {
		details = nil
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_local (occurred_at, user_id, role, clinic_id, action, object_type, object_id, result, details, correlation_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		rec.OccurredAt, nullable(rec.UserID), nullable(rec.Role), rec.ClinicID, rec.Action, rec.ObjectType,
		rec.ObjectID, rec.Result, details, nullable(rec.CorrelationID))
	if err != nil {
		return fmt.Errorf("запись audit_local: %w", err)
	}
	if _, err := j.Publish(ctx, tx, contracts.TopicAuditEvents, j.producer, rec); err != nil {
		return err
	}
	return nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
