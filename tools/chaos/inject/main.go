// Command inject sends one malformed record and proves that the running service
// wrote that exact record to its real DLT. It does not manufacture metrics.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
)

func poison(marker, typ string) *kgo.Record {
	return &kgo.Record{
		Topic: contracts.TopicLogisticsCommands, Partition: 0,
		Key: []byte(marker), Value: []byte(`{"chaos":true}`),
		// Deliberately omit message-id. The type is unique per test run, so an
		// unrelated DLT alert cannot satisfy the verification.
		Headers: []kgo.RecordHeader{{Key: messaging.HeaderMessageType, Value: []byte(typ)}},
	}
}

func matches(r *kgo.Record, marker, typ string) bool {
	if r.Topic != contracts.TopicLogisticsCommands+contracts.DLTSuffix || string(r.Key) != marker {
		return false
	}
	var source, cause, gotType string
	for _, h := range r.Headers {
		switch h.Key {
		case messaging.HeaderDLTTopic:
			source = string(h.Value)
		case messaging.HeaderDLTError:
			cause = string(h.Value)
		case messaging.HeaderMessageType:
			gotType = string(h.Value)
		}
	}
	return source == contracts.TopicLogisticsCommands && cause != "" && gotType == typ
}

func run(ctx context.Context) error {
	marker, typ := os.Getenv("CHAOS_MARKER"), os.Getenv("CHAOS_TYPE")
	if marker == "" || typ == "" {
		return fmt.Errorf("CHAOS_MARKER and CHAOS_TYPE are required")
	}
	dlt := contracts.TopicLogisticsCommands + contracts.DLTSuffix
	client, err := kgo.NewClient(
		kgo.SeedBrokers("kafka.bs-app.svc:9092"),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{dlt: {0: kgo.NewOffset().AtStart()}}),
		kgo.RecordPartitioner(kgo.ManualPartitioner()),
		kgo.DisableAutoCommit(),
	)
	if err != nil {
		return err
	}
	defer client.Close()
	if err := client.ProduceSync(ctx, poison(marker, typ)).FirstErr(); err != nil {
		return fmt.Errorf("produce poison: %w", err)
	}
	for ctx.Err() == nil {
		fetches := client.PollRecords(ctx, 100)
		if err := fetches.Err(); err != nil {
			return fmt.Errorf("read DLT: %w", err)
		}
		for _, r := range fetches.Records() {
			if matches(r, marker, typ) {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{
					"dlt_verified": true, "topic": r.Topic, "partition": r.Partition,
					"offset": r.Offset, "marker": marker, "type": typ,
				})
			}
		}
	}
	return ctx.Err()
}

func execute() error {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	return run(ctx)
}

func main() {
	if err := execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
