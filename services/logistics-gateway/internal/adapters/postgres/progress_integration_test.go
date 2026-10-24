//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/testinfra"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/migrations"
)

func TestProgressPublishesDispatchBeforeDeliveryExactlyOnceAndHonorsCancel(t *testing.T) {
	ctx := context.Background()
	pool, err := bspgx.Connect(ctx, testinfra.Postgres(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := bspgx.Migrate(ctx, pool, messaging.Migrations, migrations.FS); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	id := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO transport_request(obligation_id,request_id,source_clinic_id,target_clinic_id,component_kind_code,quantity,temperature_regime,status,shipment_ref,next_attempt_at,created_at,updated_at) VALUES($1,$1,1,2,'RBC',1,'COLD_2_6','ACCEPTED','C03-1',$2,$2,$2)`, id, now); err != nil {
		t.Fatal(err)
	}
	store := postgres.ProgressStore{Pool: pool, Journal: messaging.NewJournal("logistics-gateway", "logistics", clock.NewMutable(now))}
	departed := now.Add(-time.Hour)
	intact := true
	temp := 4.0
	p := app.ShipmentProgress{DispatchedAt: &departed, DeliveredAt: &now, CourierRef: "courier-1", DepartureTemperature: &temp, ArrivalTemperature: &temp, DepartureContainerIntact: &intact, ArrivalContainerIntact: &intact}
	row := app.ProgressOrder{ID: id, ShipmentRef: "C03-1"}
	for range 2 {
		if err := store.SaveProgress(ctx, row, p); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := pool.Query(ctx, `SELECT message_type FROM outbox WHERE key=$1 ORDER BY occurred_at,message_id`, id.String())
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			t.Fatal(err)
		}
		types = append(types, kind)
	}
	rows.Close()
	if len(types) != 2 || types[0] != "TransportDispatched" || types[1] != "TransportDelivered" {
		t.Fatalf("неверный порядок или повтор: %v", types)
	}
	other := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO transport_request(obligation_id,request_id,source_clinic_id,target_clinic_id,component_kind_code,quantity,temperature_regime,status,shipment_ref,cancel_requested,next_attempt_at,created_at,updated_at) VALUES($1,$1,1,2,'RBC',1,'COLD_2_6','ACCEPTED','C03-2',true,$2,$2,$2)`, other, now); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProgress(ctx, app.ProgressOrder{ID: other, ShipmentRef: "C03-2"}, p); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE key=$1`, other.String()).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("отмена не подавила запоздалые факты")
	}
}
