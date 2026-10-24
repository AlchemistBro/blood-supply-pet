//go:build integration

package app_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/planning"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
)

func TestPlanningFloorRejectsDuplicatesAndOldVersions(t *testing.T) {
	e := setup(t, 500)
	ctx := context.Background()
	send := func(id uuid.UUID, version, volume int64) {
		raw, err := json.Marshal(planning.ProtectedVolumeCalculated{ClinicID: 1, ComponentKind: "RBC", VolumeUnits: volume, Version: version, ReplenishmentPlanID: uuid.New(), CalculatedAt: t0, Reason: "план"})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.svc.HandlePlanning(ctx, messaging.Message{ID: id, Type: "ProtectedVolumeCalculated", SchemaVersion: 1, OccurredAt: t0, Payload: raw}); err != nil {
			t.Fatal(err)
		}
	}
	id := uuid.New()
	send(id, 2, 4)
	send(id, 2, 4)
	send(uuid.New(), 1, 8)
	send(uuid.New(), 3, 5)
	rows := outbox(t, e.pool)
	if len(rows) != 2 || rows[0].Payload["volume_units"] != float64(4) || rows[1].Payload["volume_units"] != float64(5) {
		t.Fatalf("поздняя версия или повтор изменили порог: %+v", rows)
	}
}
