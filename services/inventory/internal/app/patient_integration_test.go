//go:build integration

package app_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"
	"github.com/AlchemistBro/blood-supply-pet/libs/k1seal/opener"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/fakeaist"
)

type patientSource struct {
	accepted map[uuid.UUID]bool
	calls    []string
	lost     bool
	received string
}

func (s *patientSource) AllocatePatient(_ context.Context, key uuid.UUID, plain, _ string, _ []string) (time.Time, error) {
	s.calls = append(s.calls, "allocate")
	s.received = plain
	s.accepted[key] = true
	if !s.lost {
		s.lost = true
		return time.Time{}, errors.New("ответ потерян после приёма")
	}
	return t0, nil
}
func (s *patientSource) ReleasePatient(context.Context, uuid.UUID, uuid.UUID) error {
	s.calls = append(s.calls, "release")
	return nil
}

func TestPatientOutboxRetryAfterRestartKeepsKeyAndReleaseOrder(t *testing.T) {
	e := setup(t, 500)
	e.aist.Add(fakeaist.Unit{AistUnitCode: "INT05-1", ClinicCode: "C002", BloodGroup: "O-"})
	e.sync(t)
	unit := unitByCode(t, e.pool, "INT05-1")
	keys, err := opener.ParseKeyring("test:" + base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	requestID, obligationID, key := uuid.New(), uuid.New(), uuid.New()
	sealed, err := keys.Seal("TEST-CASE-SECRET", requestID.String())
	if err != nil {
		t.Fatal(err)
	}
	source := &patientSource{accepted: map[uuid.UUID]bool{}}
	worker := app.PatientAllocations{Pool: e.pool, Store: postgres.New(e.pool), Source: source, Keys: keys, Journal: messaging.NewJournal("inventory", "inventory", e.clock), Clock: e.clock, Ref: refdata.MustLoad(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	send := func(payload contracts.Payload) {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		msg := messaging.Message{ID: uuid.New(), Type: payload.MessageType(), SchemaVersion: 1, OccurredAt: e.clock.Now(), Payload: raw}
		for range 2 {
			if err := worker.Handle(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
		}
	}
	send(inventory.AllocateUnitsToPatient{ObligationID: obligationID, RequestID: requestID, IdempotencyKey: key, UnitIDs: []uuid.UUID{unit.ID}, TargetClinicID: 1, PatientCaseSealed: string(sealed)})
	if worked, err := worker.DeliverOne(context.Background()); !worked || err != nil {
		t.Fatalf("первая доставка: %v %v", worked, err)
	}
	if worked, err := worker.DeliverOne(context.Background()); worked || err != nil {
		t.Fatal("повтор произошёл до срока backoff")
	}
	e.clock.Advance(10 * time.Second)
	restarted := worker
	if worked, err := restarted.DeliverOne(context.Background()); !worked || err != nil {
		t.Fatalf("повтор после перезапуска: %v %v", worked, err)
	}
	send(inventory.ReleasePatientAllocation{ObligationID: obligationID, IdempotencyKey: uuid.New(), Reason: inventory.ReleaseObligationCancelled})
	for range 1 {
		if worked, err := restarted.DeliverOne(context.Background()); !worked || err != nil {
			t.Fatalf("повтор после перезапуска: %v %v", worked, err)
		}
	}
	if len(source.accepted) != 1 || strings.Join(source.calls, ",") != "allocate,allocate,release" || source.received != "TEST-CASE-SECRET" {
		t.Fatalf("порядок или ключ нарушен: %v", source.calls)
	}
	// Отмена до доставки не закрепляет единицу и не создаёт ложной квитанции.
	cancelledObligation := uuid.New()
	send(inventory.AllocateUnitsToPatient{ObligationID: cancelledObligation, RequestID: requestID, IdempotencyKey: uuid.New(), UnitIDs: []uuid.UUID{unit.ID}, TargetClinicID: 1, PatientCaseSealed: string(sealed)})
	send(inventory.ReleasePatientAllocation{ObligationID: cancelledObligation, IdempotencyKey: uuid.New(), Reason: inventory.ReleaseObligationCancelled})
	for range 2 {
		if worked, err := restarted.DeliverOne(context.Background()); !worked || err != nil {
			t.Fatalf("компенсация до отправки: %v %v", worked, err)
		}
	}
	if len(source.accepted) != 1 || strings.Join(source.calls, ",") != "allocate,allocate,release,release" {
		t.Fatal("отменённое закрепление повторно отправлено")
	}
	var acknowledgements, leaks int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox WHERE message_type='PatientAllocationAcknowledged'`).Scan(&acknowledgements); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM aist_outbox_message WHERE payload::text LIKE '%TEST-CASE-SECRET%'`).Scan(&leaks); err != nil {
		t.Fatal(err)
	}
	if acknowledgements != 1 || leaks != 0 {
		t.Fatalf("квитанций %d, открытых номеров %d", acknowledgements, leaks)
	}
}
