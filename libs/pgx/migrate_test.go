package pgx_test

import (
	"io/fs"
	"testing"
	"testing/fstest"

	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
)

func TestMergeFSOrdersAndRejectsDuplicates(t *testing.T) {
	platform := fstest.MapFS{"00001_platform.sql": {Data: []byte("-- +goose Up\n")}}
	service := fstest.MapFS{"00100_service.sql": {Data: []byte("-- +goose Up\n")}}

	merged, err := bspgx.MergeFS(service, platform)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(merged, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != "00001_platform.sql" || entries[1].Name() != "00100_service.sql" {
		t.Fatalf("неверный порядок: %v", entries)
	}
	data, err := fs.ReadFile(merged, "00100_service.sql")
	if err != nil || string(data) != "-- +goose Up\n" {
		t.Fatalf("чтение файла: %v %q", err, data)
	}

	dup := fstest.MapFS{"00001_platform.sql": {Data: []byte("x")}}
	if _, err := bspgx.MergeFS(platform, dup); err == nil {
		t.Fatal("дубликат имени миграции должен быть ошибкой")
	}
}
