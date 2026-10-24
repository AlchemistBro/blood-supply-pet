//go:build integration

package pgx_test

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testinfra"
)

// База сервиса, поднятая до появления платформенной миграции 00003 (в ней уже 00001, 00002 и сервисная 00100),
// при следующем запуске получает 00003: платформенный диапазон 00001–00099 пополняется не по порядку.
// Без этого существующий том сервиса audit не стартовал бы после выхода ref_unit_status.
func TestPlatformMigrationAddedOutOfOrder(t *testing.T) {
	ctx := context.Background()
	pool, err := bspgx.Connect(ctx, testinfra.Postgres(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	service := fstest.MapFS{"00100_service.sql": {Data: []byte(
		"-- +goose Up\nCREATE TABLE service_probe (id int PRIMARY KEY);\n-- +goose Down\nDROP TABLE service_probe;\n")}}

	// Состояние «до»: платформа без 00003 плюс сервисная миграция.
	oldRefdata := fstest.MapFS{}
	newRefdata := fstest.MapFS{}
	entries, err := refdataEntries()
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range entries {
		newRefdata[name] = &fstest.MapFile{Data: data}
		if name != "00003_ref_unit_status.sql" {
			oldRefdata[name] = &fstest.MapFile{Data: data}
		}
	}
	if _, ok := newRefdata["00003_ref_unit_status.sql"]; !ok {
		t.Fatal("в libs/refdata нет миграции 00003_ref_unit_status.sql")
	}
	if _, err := bspgx.Migrate(ctx, pool, messaging.Migrations, oldRefdata, service); err != nil {
		t.Fatalf("исходное состояние: %v", err)
	}

	applied, err := bspgx.Migrate(ctx, pool, messaging.Migrations, newRefdata, service)
	if err != nil {
		t.Fatalf("миграция не по порядку не применилась: %v", err)
	}
	if len(applied) != 1 || applied[0] != "00003_ref_unit_status.sql" {
		t.Fatalf("применено %v, ожидали только 00003", applied)
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
		t.Fatalf("справочник статусов единицы не засеян: %d", statuses)
	}
}

func refdataEntries() (map[string][]byte, error) {
	entries, err := fs.ReadDir(refdata.Migrations, ".")
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, e := range entries {
		data, err := fs.ReadFile(refdata.Migrations, e.Name())
		if err != nil {
			return nil, err
		}
		out[e.Name()] = data
	}
	return out, nil
}
