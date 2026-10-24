//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSagaFiftyConcurrentConfirmationsAllocateOneUnit(t *testing.T) {
	u := newUnit(t)
	token := login(t, "operator-c001")
	ids := make([]string, 50)
	for i := range ids {
		ids[i] = openObligation(t, 1).ObligationID
	}
	type outcome struct {
		id, code string
		status   int
		err      error
	}
	results := make(chan outcome, len(ids))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			body, err := json.Marshal(map[string]any{"unit_ids": []string{u.UnitID}, "idempotency_key": uuid.NewString()})
			if err != nil {
				results <- outcome{err: err}
				return
			}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, transferURL+"/obligations/"+id+"/confirm", bytes.NewReader(body))
			if err != nil {
				results <- outcome{err: err}
				return
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+token)
			response, err := httpClient.Do(request)
			if err != nil {
				results <- outcome{err: err}
				return
			}
			defer func() { _ = response.Body.Close() }()
			var result struct {
				Code string `json:"code"`
			}
			err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result)
			results <- outcome{id: id, code: result.Code, status: response.StatusCode, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	winner := ""
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.status >= 500 {
			t.Fatalf("подтверждение не выдержало конкуренцию: %d", result.status)
		}
		if result.code == "CONFIRMED" {
			winners++
			winner = result.id
		}
	}
	if winners != 1 {
		t.Fatalf("одна единица подтверждена %d раз", winners)
	}
	var active int
	if err := dbQueryRow(t, "transfer", `SELECT count(*) FROM allocation WHERE unit_id=$1 AND status='CONFIRMED'`, []any{u.UnitID}, &active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("активных закреплений %d", active)
	}
	awaitStatus(t, winner, "TRANSPORT_REQUESTED")
	eventually(t, sagaTimeout, "ровно один заказ победителя", func() (bool, string) { n := len(shipments(t, winner)); return n == 1, fmt.Sprint(n) })
}

func TestSagaKafkaPausePreservesConfirmationAndSingleTransport(t *testing.T) {
	u := newUnit(t)
	o := openObligation(t, 1)
	compose(t, "pause", "kafka")
	paused := true
	t.Cleanup(func() {
		if paused {
			compose(t, "unpause", "kafka")
		}
	})
	confirm(t, o.ObligationID, u)
	current := getObligation(t, serviceToken(t), o.ObligationID)
	if current.Status != "CONFIRMED" {
		t.Fatalf("остановка Kafka изменила подтверждение: %s", current.Status)
	}
	var pending int
	if err := dbQueryRow(t, "transfer", `SELECT count(*) FROM outbox WHERE message_type='RequestTransport' AND key=$1 AND published_at IS NULL`, []any{o.ObligationID}, &pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("команда не сохранена в outbox: %d", pending)
	}
	time.Sleep(time.Minute)
	compose(t, "unpause", "kafka")
	paused = false
	awaitStatus(t, o.ObligationID, "TRANSPORT_REQUESTED")
	eventually(t, sagaTimeout, "один заказ после восстановления Kafka", func() (bool, string) { n := len(shipments(t, o.ObligationID)); return n == 1, fmt.Sprint(n) })
}
