// Package messaging — платформа обмена сообщениями (ADR-0003): transactional outbox,
// ретранслятор в Kafka, inbox для идемпотентности, consumer с повторами и DLT.
// Бизнес-код никогда не пишет в Kafka напрямую: только через Journal в транзакции.
package messaging

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Заголовки Kafka-сообщения (docs/architecture/events.md).
const (
	HeaderMessageID     = "message-id"
	HeaderMessageType   = "message-type"
	HeaderSchemaVersion = "schema-version"
	HeaderCorrelationID = "correlation-id"
	HeaderCausationID   = "causation-id"
	HeaderProducer      = "producer"
	HeaderOccurredAt    = "occurred-at"
	HeaderDLTError      = "dlt-error"
	HeaderDLTTopic      = "dlt-source-topic"
)

// Message — сообщение в транспортном виде: заголовки плюс JSON-тело.
type Message struct {
	ID            uuid.UUID
	Type          string
	SchemaVersion int
	Topic         string
	Key           string
	Payload       json.RawMessage
	CorrelationID string
	CausationID   string
	Producer      string
	OccurredAt    time.Time
}

// Decode разбирает тело в v.
func (m Message) Decode(v any) error {
	if err := json.Unmarshal(m.Payload, v); err != nil {
		return fmt.Errorf("разбор %s: %w", m.Type, err)
	}
	return nil
}

// Record превращает сообщение в запись Kafka. Ключ — идентификатор агрегата.
func (m Message) Record() *kgo.Record {
	return &kgo.Record{
		Topic: m.Topic,
		Key:   []byte(m.Key),
		Value: m.Payload,
		Headers: []kgo.RecordHeader{
			{Key: HeaderMessageID, Value: []byte(m.ID.String())},
			{Key: HeaderMessageType, Value: []byte(m.Type)},
			{Key: HeaderSchemaVersion, Value: []byte(strconv.Itoa(m.SchemaVersion))},
			{Key: HeaderCorrelationID, Value: []byte(m.CorrelationID)},
			{Key: HeaderCausationID, Value: []byte(m.CausationID)},
			{Key: HeaderProducer, Value: []byte(m.Producer)},
			{Key: HeaderOccurredAt, Value: []byte(m.OccurredAt.UTC().Format(time.RFC3339Nano))},
		},
	}
}

// FromRecord восстанавливает сообщение из записи Kafka. Без message-id и message-type
// сообщение считается ядовитым: без них невозможна идемпотентность.
func FromRecord(r *kgo.Record) (Message, error) {
	m := Message{Topic: r.Topic, Key: string(r.Key), Payload: r.Value, SchemaVersion: 1}
	for _, h := range r.Headers {
		v := string(h.Value)
		switch h.Key {
		case HeaderMessageID:
			id, err := uuid.Parse(v)
			if err != nil {
				return m, fmt.Errorf("заголовок %s: %w", HeaderMessageID, err)
			}
			m.ID = id
		case HeaderMessageType:
			m.Type = v
		case HeaderSchemaVersion:
			if n, err := strconv.Atoi(v); err == nil {
				m.SchemaVersion = n
			}
		case HeaderCorrelationID:
			m.CorrelationID = v
		case HeaderCausationID:
			m.CausationID = v
		case HeaderProducer:
			m.Producer = v
		case HeaderOccurredAt:
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				m.OccurredAt = t
			}
		}
	}
	if m.ID == uuid.Nil {
		return m, fmt.Errorf("сообщение без заголовка %s", HeaderMessageID)
	}
	if m.Type == "" {
		return m, fmt.Errorf("сообщение без заголовка %s", HeaderMessageType)
	}
	return m, nil
}
