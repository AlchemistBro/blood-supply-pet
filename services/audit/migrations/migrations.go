// Package migrations встраивает SQL-миграции сервиса audit (goose, версии 00100+).
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var files embed.FS

// FS — миграции сервиса в корне файловой системы (так читает goose).
var FS fs.FS = files
