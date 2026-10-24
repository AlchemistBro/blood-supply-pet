// Package migrations встраивает SQL-миграции сервиса matching (goose, версии 00100+; 00001-00099 - платформа).
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var files embed.FS

// FS - миграции сервиса в корне файловой системы (так читает goose).
var FS fs.FS = files
