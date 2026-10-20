package messaging

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"

	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
)

// Producer — то, что умеет синхронно отправить одну запись. kgo.Client подходит через KafkaProducer;
// в тестах подменяется, чтобы проверить остановку на первой ошибке.
type Producer interface {
	ProduceSync(ctx context.Context, rec *kgo.Record) error
}

// KafkaProducer оборачивает kgo.Client.
type KafkaProducer struct{ Client *kgo.Client }

// ProduceSync отправляет запись и ждёт подтверждения всех реплик (acks=all, идемпотентный продюсер).
func (p KafkaProducer) ProduceSync(ctx context.Context, rec *kgo.Record) error {
	return p.Client.ProduceSync(ctx, rec).FirstErr()
}

// RelayConfig — настройки ретранслятора.
type RelayConfig struct {
	// Interval — пауза между опросами outbox. 200 мс достаточно для стенда (ADR-0003).
	Interval time.Duration
	// BatchSize — сколько строк брать за один проход.
	BatchSize int
	// LockKey — ключ advisory-блокировки: ретранслятор работает в одном экземпляре на базу.
	LockKey int64
}

// DefaultRelayConfig — значения по умолчанию.
func DefaultRelayConfig() RelayConfig {
	return RelayConfig{Interval: 200 * time.Millisecond, BatchSize: 100, LockKey: 0x6F7574626F78} // "outbox"
}

// Relay читает outbox по порядку id и отправляет в Kafka. Останавливается на первой ошибке:
// иначе сообщения по одному агрегату могли бы обогнать друг друга. Один активный экземпляр на базу.
type Relay struct {
	pool     *pgxpool.Pool
	producer Producer
	cfg      RelayConfig
	log      *slog.Logger
	metrics  RelayMetrics
}

// RelayMetrics — точки для метрик; по умолчанию ничего не делают.
type RelayMetrics struct {
	Published   func(n int)
	Failed      func()
	Unpublished func(count int64, oldest time.Duration)
}

// NewRelay создаёт ретранслятор.
func NewRelay(pool *pgxpool.Pool, producer Producer, cfg RelayConfig, log *slog.Logger) *Relay {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultRelayConfig().Interval
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultRelayConfig().BatchSize
	}
	if cfg.LockKey == 0 {
		cfg.LockKey = DefaultRelayConfig().LockKey
	}
	return &Relay{pool: pool, producer: producer, cfg: cfg, log: log.With("component", "outbox-relay")}
}

// WithMetrics подключает метрики.
func (r *Relay) WithMetrics(m RelayMetrics) *Relay {
	r.metrics = m
	return r
}

// Run держит advisory-блокировку и опрашивает outbox до отмены контекста.
// Если блокировка занята другим экземпляром, ждёт и пробует снова.
func (r *Relay) Run(ctx context.Context) error {
	for {
		lease, err := bspgx.TryAdvisoryLock(ctx, r.pool, r.cfg.LockKey)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			r.log.Warn("не удалось взять блокировку ретранслятора", "err", err)
			if !sleep(ctx, r.cfg.Interval*10) {
				return nil
			}
			continue
		}
		if lease == nil {
			if !sleep(ctx, r.cfg.Interval*10) {
				return nil
			}
			continue
		}
		r.log.Info("ретранслятор outbox активен")
		r.loop(ctx, lease)
		lease.Release(context.WithoutCancel(ctx))
		if ctx.Err() != nil {
			return nil
		}
	}
}

func (r *Relay) loop(ctx context.Context, lease *bspgx.Lease) {
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !lease.Alive(ctx) {
			r.log.Warn("соединение блокировки потеряно, ретранслятор перезапускается")
			return
		}
		for {
			n, err := r.DrainOnce(ctx)
			if err != nil {
				if ctx.Err() == nil {
					r.log.Error("ретранслятор остановлен на ошибке, повтор через интервал", "err", err)
				}
				break
			}
			if n < r.cfg.BatchSize {
				break
			}
		}
		r.reportLag(ctx)
	}
}

type outboxRow struct {
	ID            int64
	MessageID     uuid.UUID
	Topic         string
	Key           string
	MessageType   string
	SchemaVersion int
	Payload       []byte
	CorrelationID *string
	CausationID   *string
	Producer      string
	OccurredAt    time.Time
}

// DrainOnce отправляет одну пачку неопубликованных сообщений по порядку id. Возвращает число
// отправленных; при ошибке останавливается, уже отправленные помечены опубликованными.
// Экспортирован для тестов и для ручного прогона; порядок гарантирует только один вызывающий.
func (r *Relay) DrainOnce(ctx context.Context) (int, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, message_id, topic, key, message_type, schema_version, payload,
		       correlation_id, causation_id, producer, occurred_at
		FROM outbox WHERE published_at IS NULL ORDER BY id LIMIT $1`, r.cfg.BatchSize)
	if err != nil {
		return 0, fmt.Errorf("чтение outbox: %w", err)
	}
	batch, err := pgx.CollectRows(rows, pgx.RowToStructByPos[outboxRow])
	if err != nil {
		return 0, fmt.Errorf("разбор outbox: %w", err)
	}
	sent := 0
	for _, row := range batch {
		msg := Message{
			ID: row.MessageID, Type: row.MessageType, SchemaVersion: row.SchemaVersion, Topic: row.Topic,
			Key: row.Key, Payload: row.Payload, Producer: row.Producer, OccurredAt: row.OccurredAt,
		}
		if row.CorrelationID != nil {
			msg.CorrelationID = *row.CorrelationID
		}
		if row.CausationID != nil {
			msg.CausationID = *row.CausationID
		}
		if err := r.producer.ProduceSync(ctx, msg.Record()); err != nil {
			if r.metrics.Failed != nil {
				r.metrics.Failed()
			}
			return sent, fmt.Errorf("отправка %s id=%d: %w", msg.Type, row.ID, err)
		}
		// Отметка после отправки: при падении между ними сообщение уйдёт повторно, дубль отбросит inbox.
		if _, err := r.pool.Exec(ctx, `UPDATE outbox SET published_at = now() WHERE id = $1`, row.ID); err != nil {
			return sent, fmt.Errorf("отметка публикации id=%d: %w", row.ID, err)
		}
		sent++
	}
	if sent > 0 && r.metrics.Published != nil {
		r.metrics.Published(sent)
	}
	return sent, nil
}

func (r *Relay) reportLag(ctx context.Context) {
	if r.metrics.Unpublished == nil {
		return
	}
	var count int64
	var oldest *time.Time
	err := r.pool.QueryRow(ctx,
		`SELECT count(*), min(occurred_at) FROM outbox WHERE published_at IS NULL`).Scan(&count, &oldest)
	if err != nil {
		return
	}
	var age time.Duration
	if oldest != nil {
		age = time.Since(*oldest)
	}
	r.metrics.Unpublished(count, age)
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// ErrStopped — ретранслятор остановлен по контексту.
var ErrStopped = errors.New("ретранслятор остановлен")
