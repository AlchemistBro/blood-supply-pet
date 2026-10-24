// Package postgres — хранилище журнала аудита на sqlc-запросах.
package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/services/audit/internal/adapters/postgres/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/audit/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/audit/internal/domain"
)

// Store реализует app.Store.
type Store struct {
	pool *pgxpool.Pool
}

// New создаёт хранилище.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Append блокирует голову цепочки (SELECT … FOR UPDATE), связывает запись и сохраняет её вместе с новой головой.
func (s *Store) Append(ctx context.Context, tx pgx.Tx, link func(headSeq int64, headHash []byte) (domain.Record, error)) (domain.Record, error) {
	q := gen.New(tx)
	head, err := q.LockChainHead(ctx)
	if err != nil {
		return domain.Record{}, fmt.Errorf("блокировка головы цепочки: %w", err)
	}
	rec, err := link(head.Seq, head.Hash)
	if err != nil {
		return domain.Record{}, err
	}
	var details []byte
	if rec.Details != nil {
		if details, err = json.Marshal(rec.Details); err != nil {
			return domain.Record{}, fmt.Errorf("details: %w", err)
		}
	}
	if err := q.InsertRecord(ctx, gen.InsertRecordParams{
		AuditRecordID: rec.ID, Seq: rec.Seq,
		OccurredAt: pgtype.Timestamptz{Time: rec.OccurredAt, Valid: true},
		ReceivedAt: pgtype.Timestamptz{Time: rec.ReceivedAt, Valid: true},
		UserID:     nullable(rec.UserID), Role: nullable(rec.Role), ClinicID: rec.ClinicID,
		Module: rec.Module, Action: rec.Action, ObjectType: rec.ObjectType, ObjectID: rec.ObjectID, Result: rec.Result,
		Details: details, CorrelationID: nullable(rec.CorrelationID), Producer: rec.Producer,
		SourceMessageID: rec.SourceMessageID, PrevHash: rec.PrevHash, Hash: rec.Hash,
	}); err != nil {
		return domain.Record{}, fmt.Errorf("запись аудита: %w", err)
	}
	if err := q.UpdateChainHead(ctx, gen.UpdateChainHeadParams{Seq: rec.Seq, Hash: rec.Hash}); err != nil {
		return domain.Record{}, fmt.Errorf("голова цепочки: %w", err)
	}
	return rec, nil
}

// List — страница по фильтру.
func (s *Store) List(ctx context.Context, f app.Filter) ([]domain.Record, error) {
	rows, err := gen.New(s.pool).ListRecords(ctx, gen.ListRecordsParams{
		Module: f.Module, Action: f.Action, ObjectType: f.ObjectType, ObjectID: f.ObjectID, UserID: f.UserID,
		OccurredFrom: optTime(f.From), OccurredTo: optTime(f.To), AfterSeq: f.AfterSeq, PageLimit: clampInt32(f.Limit),
	})
	if err != nil {
		return nil, fmt.Errorf("чтение журнала: %w", err)
	}
	return toDomain(rows)
}

// Walk обходит журнал по порядку пачками.
func (s *Store) Walk(ctx context.Context, fromSeq int64, batch int, fn func(domain.Record) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := gen.New(tx)
	var headSeq int64
	var headHash []byte
	if err := tx.QueryRow(ctx, `SELECT seq,hash FROM audit_chain_head WHERE id=1`).Scan(&headSeq, &headHash); err != nil {
		return err
	}
	lastHash := domain.GenesisHash
	for {
		rows, err := q.RecordsFromSeq(ctx, gen.RecordsFromSeqParams{Seq: fromSeq, Limit: clampInt32(batch)})
		if err != nil {
			return fmt.Errorf("обход журнала: %w", err)
		}
		recs, err := toDomain(rows)
		if err != nil {
			return err
		}
		for _, r := range recs {
			if err := fn(r); err != nil {
				return err
			}
			fromSeq = r.Seq
			lastHash = r.Hash
		}
		if len(rows) < batch {
			if fromSeq != headSeq || !bytes.Equal(lastHash, headHash) {
				return &domain.ChainBreakError{Report: domain.BreakReport{Seq: fromSeq + 1, Reason: "хвост журнала не совпадает с сохранённой головой цепочки"}}
			}
			return tx.Commit(ctx)
		}
	}
}

func toDomain(rows []gen.AuditRecord) ([]domain.Record, error) {
	out := make([]domain.Record, 0, len(rows))
	for _, r := range rows {
		rec := domain.Record{
			ID: r.AuditRecordID, Seq: r.Seq, OccurredAt: r.OccurredAt.Time, ReceivedAt: r.ReceivedAt.Time,
			UserID: deref(r.UserID), Role: deref(r.Role), ClinicID: r.ClinicID, Module: r.Module, Action: r.Action,
			ObjectType: r.ObjectType, ObjectID: r.ObjectID, Result: r.Result, CorrelationID: deref(r.CorrelationID),
			Producer: r.Producer, SourceMessageID: r.SourceMessageID, PrevHash: r.PrevHash, Hash: r.Hash,
		}
		if len(r.Details) > 0 {
			if err := json.Unmarshal(r.Details, &rec.Details); err != nil {
				return nil, fmt.Errorf("details записи %d: %w", r.Seq, err)
			}
		}
		out = append(out, rec)
	}
	return out, nil
}

// clampInt32 приводит int к int32 с ограничением диапазона: лимиты страниц малы, но преобразование безопасно всегда.
func clampInt32(n int) int32 {
	if n < 0 {
		return 0
	}
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(n)
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

func optTime(t *pgtypeTime) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}
