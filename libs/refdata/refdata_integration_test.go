//go:build integration

package refdata_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testinfra"
)

func TestSeedIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pool, err := bspgx.Connect(ctx, testinfra.Postgres(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	applied, err := bspgx.Migrate(ctx, pool, messaging.Migrations, refdata.Migrations)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 3 {
		t.Fatalf("применено миграций %d: %v", len(applied), applied)
	}
	set := refdata.MustLoad()
	for i := 0; i < 2; i++ {
		if err := bspgx.WithTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error { return set.Seed(ctx, tx) }); err != nil {
			t.Fatal(err)
		}
	}
	var clinics, versions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ref_clinic`).Scan(&clinics); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM refdata_version`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if clinics != 100 || versions != 1 {
		t.Fatalf("клиник %d, версий %d", clinics, versions)
	}
	var statuses, available int
	if err := pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE is_available) FROM ref_unit_status`).Scan(&statuses, &available); err != nil {
		t.Fatal(err)
	}
	if statuses != 5 || available != 1 {
		t.Fatalf("статусов единицы %d, доступных %d: ожидали 5 и 1", statuses, available)
	}
}

// База, засеянная прошлой версией справочников, получает новые таблицы и записи при следующем запуске:
// иначе сервисы стенда, поднятые до появления ref_unit_status, остались бы с пустым справочником.
func TestSeedUpgradesOlderVersion(t *testing.T) {
	ctx := context.Background()
	pool, err := bspgx.Connect(ctx, testinfra.Postgres(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := bspgx.Migrate(ctx, pool, messaging.Migrations, refdata.Migrations); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO refdata_version (version, applied_at) VALUES (1, now())`); err != nil {
		t.Fatal(err)
	}
	set := refdata.MustLoad()
	if err := bspgx.WithTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error { return set.Seed(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
	var statuses int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ref_unit_status`).Scan(&statuses); err != nil {
		t.Fatal(err)
	}
	if statuses != 5 {
		t.Fatalf("после обновления версии статусов единицы %d", statuses)
	}
}
