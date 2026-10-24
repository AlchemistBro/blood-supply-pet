//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testinfra"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/migrations"
)

var t0 = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)

func setup(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pool, err := bspgx.Connect(ctx, testinfra.Postgres(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := bspgx.Migrate(ctx, pool, messaging.Migrations, refdata.Migrations, migrations.FS); err != nil {
		t.Fatal(err)
	}
	ref := refdata.MustLoad()
	if err := bspgx.WithTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error { return ref.Seed(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
	return pool
}

func unit(id uuid.UUID, status string, version int64) domain.Unit {
	u := domain.Unit{UnitID: id, StatusReceivedAt: t0}
	u.AistUnitCode = "U-1"
	u.ClinicID = 1
	u.ComponentKind = "RBC"
	u.BloodGroup = "A+"
	u.VolumeML = 300
	u.ExpiresAt = t0.Add(30 * 24 * time.Hour)
	u.Status = status
	u.Version = version
	return u
}

// Условие версии стоит в самом UPDATE: даже без блокировки и проверки в домене старая или та же версия
// не перезаписывает новую. Снятие условия `source_version < $new` роняет этот тест.
func TestUpdateUnitIfNewerRejectsOlderAndSameVersion(t *testing.T) {
	pool := setup(t)
	store := postgres.New(pool)
	ctx := context.Background()
	id := uuid.Must(uuid.NewV7())
	err := bspgx.WithTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		ok, err := store.InsertUnit(ctx, tx, unit(id, domain.StatusIssued, 5))
		if err != nil || !ok {
			t.Fatalf("вставка: %v %v", ok, err)
		}
		for _, v := range []int64{4, 5} {
			ok, err := store.UpdateUnitIfNewer(ctx, tx, unit(id, domain.StatusInStock, v))
			if err != nil {
				return err
			}
			if ok {
				t.Errorf("версия %d перезаписала версию 5", v)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	units, err := store.UnitsByIDs(ctx, []uuid.UUID{id})
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 || units[0].Status != domain.StatusIssued || units[0].Version != 5 {
		t.Fatalf("единица после старых версий: %+v", units)
	}

	err = bspgx.WithTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		ok, err := store.UpdateUnitIfNewer(ctx, tx, unit(id, domain.StatusDiscarded, 6))
		if err == nil && !ok {
			t.Error("новая версия 6 не применилась")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
