// Package domain — цепочка хешей журнала аудита. Чистый пакет: ни БД, ни Kafka, ни HTTP.
// Каждая запись содержит хеш предыдущей, поэтому вставка, удаление или правка любой записи
// ломает все последующие хеши и обнаруживается проверкой (NFR008).
package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// GenesisHash — хеш «до первой записи»: 32 нулевых байта.
var GenesisHash = make([]byte, sha256.Size)

// Record — запись журнала в том виде, в каком она хешируется и хранится.
type Record struct {
	ID              uuid.UUID
	Seq             int64
	OccurredAt      time.Time
	ReceivedAt      time.Time
	UserID          string
	Role            string
	ClinicID        *int64
	Module          string
	Action          string
	ObjectType      string
	ObjectID        string
	Result          string
	Details         map[string]any
	CorrelationID   string
	Producer        string
	SourceMessageID uuid.UUID
	PrevHash        []byte
	Hash            []byte
}

// canonical — поля в фиксированном порядке; map внутри encoding/json сортирует ключи,
// поэтому представление детерминировано.
type canonical struct {
	ID              string         `json:"id"`
	Seq             int64          `json:"seq"`
	OccurredAt      string         `json:"occurred_at"`
	UserID          string         `json:"user_id"`
	Role            string         `json:"role"`
	ClinicID        *int64         `json:"clinic_id"`
	Module          string         `json:"module"`
	Action          string         `json:"action"`
	ObjectType      string         `json:"object_type"`
	ObjectID        string         `json:"object_id"`
	Result          string         `json:"result"`
	Details         map[string]any `json:"details"`
	CorrelationID   string         `json:"correlation_id"`
	Producer        string         `json:"producer"`
	SourceMessageID string         `json:"source_message_id"`
}

// ComputeHash возвращает sha256(prevHash || canonical(record)).
func ComputeHash(prevHash []byte, r Record) ([]byte, error) {
	body, err := json.Marshal(canonical{
		ID: r.ID.String(), Seq: r.Seq, OccurredAt: canonicalTime(r.OccurredAt),
		UserID: r.UserID, Role: r.Role, ClinicID: r.ClinicID, Module: r.Module, Action: r.Action,
		ObjectType: r.ObjectType, ObjectID: r.ObjectID, Result: r.Result, Details: r.Details,
		CorrelationID: r.CorrelationID, Producer: r.Producer, SourceMessageID: r.SourceMessageID.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("каноническое представление записи %d: %w", r.Seq, err)
	}
	h := sha256.New()
	h.Write(prevHash)
	h.Write(body)
	return h.Sum(nil), nil
}

// canonicalTime — время в фиксированном виде с точностью до микросекунд.
// PostgreSQL хранит timestamptz с микросекундами: если хешировать наносекунды, сохранённая запись
// не совпадёт с собственным хешем и проверка сочтёт её изменённой (найдено e2e-тестом).
func canonicalTime(t time.Time) string {
	return t.UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z")
}

// Link присваивает записи следующий номер и хеш поверх головы цепочки.
func Link(headSeq int64, headHash []byte, r Record) (Record, error) {
	if len(headHash) != sha256.Size {
		return Record{}, fmt.Errorf("хеш головы цепочки имеет длину %d", len(headHash))
	}
	r.OccurredAt = r.OccurredAt.UTC().Truncate(time.Microsecond)
	r.Seq = headSeq + 1
	r.PrevHash = headHash
	hash, err := ComputeHash(headHash, r)
	if err != nil {
		return Record{}, err
	}
	r.Hash = hash
	return r, nil
}

// ErrChainBroken — цепочка нарушена.
var ErrChainBroken = errors.New("цепочка аудита нарушена")

// BreakReport — где и почему цепочка порвалась.
type BreakReport struct {
	Seq    int64
	Reason string
}

// Verifier проверяет записи, поступающие по порядку seq.
type Verifier struct {
	expectedSeq int64
	prevHash    []byte
	checked     int64
}

// NewVerifier начинает проверку с генезиса.
func NewVerifier() *Verifier {
	return &Verifier{expectedSeq: 1, prevHash: GenesisHash}
}

// Next проверяет очередную запись. Возвращает отчёт о разрыве или nil.
func (v *Verifier) Next(r Record) (*BreakReport, error) {
	if r.Seq != v.expectedSeq {
		return &BreakReport{Seq: r.Seq, Reason: fmt.Sprintf("ожидали seq %d, получили %d: запись пропущена или вставлена", v.expectedSeq, r.Seq)}, nil
	}
	if !bytes.Equal(r.PrevHash, v.prevHash) {
		return &BreakReport{Seq: r.Seq, Reason: "prev_hash не совпадает с хешем предыдущей записи"}, nil
	}
	want, err := ComputeHash(v.prevHash, r)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(want, r.Hash) {
		return &BreakReport{Seq: r.Seq, Reason: "хеш записи не совпадает с содержимым: запись изменена"}, nil
	}
	v.prevHash = r.Hash
	v.expectedSeq++
	v.checked++
	return nil, nil
}

// Checked — сколько записей проверено.
func (v *Verifier) Checked() int64 { return v.checked }

// HeadHash — хеш последней проверенной записи.
func (v *Verifier) HeadHash() []byte { return v.prevHash }
