package messaging

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	"github.com/AlchemistBro/blood-supply-pet/libs/correlation"
)

// Handler обрабатывает одно сообщение. Внутри обработчик открывает транзакцию, вызывает FirstTime
// и делает бизнес-работу; ошибка означает повтор с паузой, после исчерпания попыток — DLT.
type Handler func(ctx context.Context, msg Message) error

// ErrPermanent — обёртка для ошибок, повтор которых бессмыслен (неверный формат, неизвестный тип):
// сообщение сразу уходит в DLT.
type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// Permanent помечает ошибку как постоянную.
func Permanent(err error) error { return permanentError{err: err} }

// IsPermanent сообщает, помечена ли ошибка как постоянная.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

// ConsumerConfig — настройки consumer-группы.
type ConsumerConfig struct {
	Brokers    []string
	Group      string
	Topics     []string
	MaxRetries int
	MinBackoff time.Duration
	MaxBackoff time.Duration
	// PollMax — максимум записей за один опрос.
	PollMax int
	// StartAtEnd — новая группа (без закоммиченного offset) читает только новые записи, а не топик с начала.
	// Нужно потребителям «только для показа» (web: живые статусы по SSE), которым история не нужна и
	// перечитывать её при первом запуске экземпляра дорого. Бизнес-обработчики оставляют false:
	// им важна каждая запись.
	StartAtEnd bool
}

func (c *ConsumerConfig) defaults() {
	if c.MaxRetries <= 0 {
		c.MaxRetries = 5
	}
	if c.MinBackoff <= 0 {
		c.MinBackoff = 200 * time.Millisecond
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = 10 * time.Second
	}
	if c.PollMax <= 0 {
		c.PollMax = 200
	}
}

// Consumer читает топики группой, обрабатывает записи по порядку внутри партиции и коммитит
// оффсеты только после успешной обработки (или отправки в DLT).
type Consumer struct {
	client  *kgo.Client
	cfg     ConsumerConfig
	handler Handler
	log     *slog.Logger
	metrics ConsumerMetrics
}

// ConsumerMetrics — точки для метрик.
type ConsumerMetrics struct {
	Handled    func(topic, msgType string, d time.Duration, err error)
	DeadLetter func(topic, msgType string)
}

// NewConsumer создаёт клиента Kafka с consumer-группой. Автокоммит выключен; ребаланс
// блокируется на время обработки опрошенных записей, чтобы коммит не ушёл не тому экземпляру.
func NewConsumer(cfg ConsumerConfig, handler Handler, log *slog.Logger, extra ...kgo.Opt) (*Consumer, error) {
	cfg.defaults()
	reset := kgo.NewOffset().AtStart()
	if cfg.StartAtEnd {
		reset = kgo.NewOffset().AtEnd()
	}
	opts := []kgo.Opt{
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ConsumerGroup(cfg.Group),
		kgo.ConsumeTopics(cfg.Topics...),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
		kgo.ConsumeResetOffset(reset),
		kgo.RequiredAcks(kgo.AllISRAcks()),
	}
	opts = append(opts, extra...)
	client, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("клиент Kafka для группы %s: %w", cfg.Group, err)
	}
	return &Consumer{client: client, cfg: cfg, handler: handler, log: log.With("component", "consumer", "group", cfg.Group)}, nil
}

// WithMetrics подключает метрики.
func (c *Consumer) WithMetrics(m ConsumerMetrics) *Consumer {
	c.metrics = m
	return c
}

// Client возвращает клиента (нужен тестам и для DLT).
func (c *Consumer) Client() *kgo.Client { return c.client }

// Close закрывает клиента.
func (c *Consumer) Close() { c.client.Close() }

// Run опрашивает Kafka до отмены контекста.
func (c *Consumer) Run(ctx context.Context) error {
	// Порядок отложенных вызовов важен: BlockRebalanceOnPoll держит ребаланс до AllowRebalance,
	// и Close() без него зависает на выходе из группы. Сначала разрешаем ребаланс, потом закрываем.
	defer c.client.Close()
	defer c.client.AllowRebalance()
	for {
		fetches := c.client.PollRecords(ctx, c.cfg.PollMax)
		if fetches.IsClientClosed() || ctx.Err() != nil {
			return nil
		}
		fetches.EachError(func(topic string, partition int32, err error) {
			if !errors.Is(err, context.Canceled) {
				c.log.Error("ошибка чтения партиции", "topic", topic, "partition", partition, "err", err)
			}
		})
		var failed error
		fetches.EachPartition(func(p kgo.FetchTopicPartition) {
			if failed != nil {
				return
			}
			for _, rec := range p.Records {
				if err := c.handle(ctx, rec); err != nil {
					failed = err
					return
				}
			}
		})
		if failed != nil {
			// Контекст отменён посреди обработки: оффсеты не коммитим, сообщения придут повторно.
			c.client.AllowRebalance()
			return nil
		}
		if err := c.client.CommitUncommittedOffsets(ctx); err != nil && ctx.Err() == nil {
			c.log.Error("коммит оффсетов", "err", err)
		}
		c.client.AllowRebalance()
	}
}

// handle обрабатывает запись с повторами. Возвращает ошибку только при отмене контекста.
func (c *Consumer) handle(ctx context.Context, rec *kgo.Record) error {
	msg, err := FromRecord(rec)
	if err != nil {
		c.log.Error("ядовитое сообщение: нет обязательных заголовков", "topic", rec.Topic, "err", err)
		return c.deadLetter(ctx, rec, err)
	}
	msgCtx := correlation.With(ctx, correlation.IDs{Correlation: msg.CorrelationID, Causation: msg.ID.String()})
	backoff := c.cfg.MinBackoff
	for attempt := 1; ; attempt++ {
		start := time.Now()
		err := c.handler(msgCtx, msg)
		if c.metrics.Handled != nil {
			c.metrics.Handled(msg.Topic, msg.Type, time.Since(start), err)
		}
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if IsPermanent(err) || attempt >= c.cfg.MaxRetries {
			c.log.Error("сообщение отправлено в DLT", "type", msg.Type, "id", msg.ID, "attempts", attempt, "err", err)
			return c.deadLetter(ctx, rec, err)
		}
		c.log.Warn("ошибка обработки, повтор", "type", msg.Type, "id", msg.ID, "attempt", attempt, "err", err)
		if !sleep(ctx, backoff) {
			return ctx.Err()
		}
		backoff = min(backoff*2, c.cfg.MaxBackoff)
	}
}

func (c *Consumer) deadLetter(ctx context.Context, rec *kgo.Record, cause error) error {
	dlt := &kgo.Record{
		Topic:   rec.Topic + contracts.DLTSuffix,
		Key:     rec.Key,
		Value:   rec.Value,
		Headers: append(append([]kgo.RecordHeader{}, rec.Headers...), kgo.RecordHeader{Key: HeaderDLTError, Value: []byte(cause.Error())}, kgo.RecordHeader{Key: HeaderDLTTopic, Value: []byte(rec.Topic)}),
	}
	if err := c.client.ProduceSync(ctx, dlt).FirstErr(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// DLT недоступен: не коммитим, сообщение вернётся; лучше повтор, чем потеря.
		return fmt.Errorf("отправка в DLT: %w", err)
	}
	if c.metrics.DeadLetter != nil {
		c.metrics.DeadLetter(rec.Topic, headerValue(rec, HeaderMessageType))
	}
	return nil
}

func headerValue(rec *kgo.Record, key string) string {
	for _, h := range rec.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}
