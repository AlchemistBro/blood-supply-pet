package messaging

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
)

// resetOffset — offset, с которого новая группа начнёт чтение. Клиент Kafka создаётся без подключения
// к брокеру, поэтому тест идёт без Docker.
func resetOffset(t *testing.T, cfg ConsumerConfig) int64 {
	t.Helper()
	c, err := NewConsumer(cfg, func(context.Context, Message) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	off, ok := c.Client().OptValue(kgo.ConsumeResetOffset).(kgo.Offset)
	if !ok {
		t.Fatal("ConsumeResetOffset не задан")
	}
	return off.EpochOffset().Offset
}

// По умолчанию новая группа читает топик с начала (бизнес-обработчики не теряют записи);
// StartAtEnd — только новые записи (потребители для показа).
func TestConsumerResetOffset(t *testing.T) {
	base := ConsumerConfig{Brokers: []string{"127.0.0.1:1"}, Group: "g", Topics: []string{"t"}}
	start, end := kgo.NewOffset().AtStart().EpochOffset().Offset, kgo.NewOffset().AtEnd().EpochOffset().Offset
	if got := resetOffset(t, base); got != start {
		t.Fatalf("по умолчанию — с начала (%d), получено %d", start, got)
	}
	base.StartAtEnd = true
	if got := resetOffset(t, base); got != end {
		t.Fatalf("StartAtEnd — с конца (%d), получено %d", end, got)
	}
}
