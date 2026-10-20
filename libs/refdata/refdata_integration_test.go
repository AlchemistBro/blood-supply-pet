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
	if len(applied) != 2 {
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
}
