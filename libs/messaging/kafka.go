package messaging

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// NewProducerClient создаёт клиента только для отправки: идемпотентный продюсер, acks=all.
func NewProducerClient(brokers []string, extra ...kgo.Opt) (*kgo.Client, error) {
	opts := []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerBatchCompression(kgo.SnappyCompression()),
		kgo.RecordRetries(10),
		kgo.ProduceRequestTimeout(10 * time.Second),
	}
	opts = append(opts, extra...)
	client, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("клиент Kafka: %w", err)
	}
	return client, nil
}

// EnsureTopics создаёт топики, если их нет (один брокер: 1 партиция на топик по умолчанию,
// но партиций может быть больше: порядок нужен только внутри ключа). Существующие пропускает.
func EnsureTopics(ctx context.Context, brokers []string, partitions int32, topics ...string) error {
	client, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		return fmt.Errorf("клиент Kafka: %w", err)
	}
	defer client.Close()
	adm := kadm.NewClient(client)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := adm.CreateTopics(ctx, partitions, 1, nil, topics...)
	if err != nil {
		return fmt.Errorf("создание топиков: %w", err)
	}
	for _, r := range resp.Sorted() {
		if r.Err != nil && !errors.Is(r.Err, kerr.TopicAlreadyExists) {
			return fmt.Errorf("топик %s: %w", r.Topic, r.Err)
		}
	}
	return nil
}

// WithDLT добавляет к списку топиков их DLT-варианты.
func WithDLT(topics ...string) []string {
	out := make([]string, 0, len(topics)*2)
	for _, t := range topics {
		out = append(out, t, t+".dlt")
	}
	return out
}

// Ping проверяет доступность брокеров (для /readyz).
func Ping(ctx context.Context, brokers []string) error {
	client, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		return err
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return client.Ping(ctx)
}
