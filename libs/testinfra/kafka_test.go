package testinfra

import (
	"context"
	"errors"
	"testing"
)

func TestKafkaProbeRetriesUntilProtocolIsReady(t *testing.T) {
	attempts := 0
	err := waitKafkaProbe(context.Background(), func(context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("broker closed the connection")
		}
		return nil
	})
	if err != nil || attempts != 3 {
		t.Fatalf("ожидали готовность после двух отказов: attempts=%d err=%v", attempts, err)
	}
}

func TestKafkaProbeStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := waitKafkaProbe(ctx, func(context.Context) error {
		cancel()
		return errors.New("not ready")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ожидали отмену, получили %v", err)
	}
}
