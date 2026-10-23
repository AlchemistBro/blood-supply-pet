// scaffold создаёт заготовку нового сервиса по анатомии из docs/architecture/repo-layout.md:
// точка входа на libs/runtime, контракт OpenAPI, миграции, слои domain/app/adapters.
//
//	go run ./tools/scaffold --name matching --module requests --port 8104
//
// Генерация идемпотентна в одну сторону: существующий каталог сервиса не перезаписывается.
package scaffold

import (
	"bytes"
	"errors"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

// Params — что подставляется в шаблоны.
type Params struct {
	// Name — имя сервиса и каталога: строчные буквы, цифры и дефис.
	Name string
	// Module — константа модуля аудита (contracts/audit.Module*), например "matching".
	Module string
	// ModulePath — путь Go-модуля репозитория.
	ModulePath string
	// Port — порт сервиса в compose (8101–8108 и далее).
	Port int
	// DBName — имя базы и роли: допускает отличие от Name (logistics-gateway использует базу logistics).
	DBName string
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)

// Validate проверяет параметры.
func (p *Params) Validate() error {
	if !nameRe.MatchString(p.Name) {
		return fmt.Errorf("имя сервиса %q: нужны строчные латинские буквы, цифры и дефис, от 2 до 31 символа", p.Name)
	}
	if p.Module == "" {
		p.Module = strings.ReplaceAll(p.Name, "-", "")
	}
	if p.DBName == "" {
		p.DBName = strings.ReplaceAll(p.Name, "-", "_")
	}
	if p.ModulePath == "" {
		return errors.New("не задан путь модуля Go")
	}
	if p.Port < 1024 || p.Port > 65535 {
		return fmt.Errorf("порт %d вне диапазона 1024-65535", p.Port)
	}
	return nil
}

// File — один генерируемый файл.
type File struct {
	Path    string
	Content []byte
}

// Render строит содержимое всех файлов в памяти и проверяет, что Go-код форматируется.
func Render(p Params) ([]File, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	var out []File
	for _, t := range templates {
		path := strings.ReplaceAll(t.path, "{{name}}", p.Name)
		tmpl, err := template.New(path).Delims("[[", "]]").Parse(t.body)
		if err != nil {
			return nil, fmt.Errorf("шаблон %s: %w", path, err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, p); err != nil {
			return nil, fmt.Errorf("шаблон %s: %w", path, err)
		}
		content := buf.Bytes()
		if strings.HasSuffix(path, ".go") {
			formatted, err := format.Source(content)
			if err != nil {
				return nil, fmt.Errorf("сгенерированный %s не компилируется синтаксически: %w", path, err)
			}
			content = formatted
		}
		out = append(out, File{Path: path, Content: content})
	}
	return out, nil
}

// Write записывает файлы под root. Если каталог сервиса уже есть, возвращает ошибку.
func Write(root string, p Params) ([]string, error) {
	files, err := Render(p)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(root, "services", p.Name)); err == nil {
		return nil, fmt.Errorf("services/%s уже существует: заготовка не перезаписывает существующий сервис", p.Name)
	}
	written := make([]string, 0, len(files))
	for _, f := range files {
		full := filepath.Join(root, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			return nil, err
		}
		if err := os.WriteFile(full, f.Content, 0o600); err != nil {
			return nil, err
		}
		written = append(written, f.Path)
	}
	return written, nil
}

// RegisterInTaskfile добавляет сервис в переменную SERVICES Taskfile, если его там ещё нет.
func RegisterInTaskfile(root, name string) (bool, error) {
	path := filepath.Join(root, "Taskfile.yml")
	b, err := os.ReadFile(path) //nolint:gosec // путь собирается из корня репозитория
	if err != nil {
		return false, err
	}
	re := regexp.MustCompile(`(?m)^(\s*SERVICES:\s*)(.*)$`)
	m := re.FindSubmatch(b)
	if m == nil {
		return false, errors.New("в Taskfile.yml не найдена переменная SERVICES")
	}
	current := strings.Fields(string(m[2]))
	for _, s := range current {
		if s == name {
			return false, nil
		}
	}
	updated := re.ReplaceAll(b, []byte("${1}"+strings.Join(append(current, name), " ")))
	return true, os.WriteFile(path, updated, 0o600)
}

type tmplFile struct{ path, body string }

// Разделители шаблонов [[ ]], чтобы не конфликтовать с {{ }} в генерируемых Go-текстах и YAML.
var templates = []tmplFile{
	{"services/{{name}}/cmd/{{name}}/main.go", `// [[.Name]] - сервис АС распределения компонентов донорской крови.
package main

import (
	"context"

	"[[.ModulePath]]/libs/runtime"
	apispec "[[.ModulePath]]/services/[[.Name]]/api"
	"[[.ModulePath]]/services/[[.Name]]/migrations"
)

func main() {
	runtime.Run(runtime.Spec{
		Name:       "[[.Name]]",
		Module:     "[[.Module]]",
		Migrations: migrations.FS,
		OpenAPI:    apispec.Spec,
		Setup: func(_ context.Context, rt *runtime.Runtime) ([]runtime.Worker, error) {
			// Здесь: собрать адаптеры, приложение, зарегистрировать маршруты на rt.Mux,
			// вернуть фоновых воркеров (consumer, таймеры).
			_ = rt
			return nil, nil
		},
	})
}
`},
	{"services/{{name}}/api/spec.go", `// Package api хранит контракт OpenAPI сервиса [[.Name]]; сервер генерируется в internal/api/gen.
package api

import _ "embed"

// Spec - текст openapi.yaml для GET /openapi.yaml.
//
//go:embed openapi.yaml
var Spec []byte
`},
	{"services/{{name}}/api/openapi.yaml", `openapi: 3.0.3
info:
  title: [[.Name]]
  version: 0.1.0
  description: Контракт сервиса [[.Name]]. Источник истины для HTTP: код сервера генерируется из этого файла.
servers:
  - url: http://localhost:[[.Port]]
paths: {}
components:
  schemas:
    Problem:
      type: object
      required: [title, status]
      properties:
        type: { type: string }
        title: { type: string }
        status: { type: integer }
        detail: { type: string }
        instance: { type: string }
        correlation_id: { type: string }
`},
	{"services/{{name}}/api/oapi-codegen.yaml", `package: gen
output: ../internal/api/gen/api.gen.go
generate:
  std-http-server: true
  strict-server: true
  models: true
`},
	{"services/{{name}}/migrations/migrations.go", `// Package migrations встраивает SQL-миграции сервиса [[.Name]] (goose, версии 00100+; 00001-00099 - платформа).
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var files embed.FS

// FS - миграции сервиса в корне файловой системы (так читает goose).
var FS fs.FS = files
`},
	{"services/{{name}}/migrations/00100_init.sql", `-- +goose Up
-- Схема сервиса [[.Name]] (база [[.DBName]]). Имена сущностей и полей - из словаря данных ПР6 (docs/architecture/data-model.md).
-- Платформенные таблицы (outbox, inbox, audit_local, ref_*) создаются миграциями 00001-00002.

-- +goose Down
`},
	{"services/{{name}}/internal/domain/doc.go", `// Package domain - чистая предметная логика сервиса [[.Name]]: агрегаты, правила, результаты операций.
// Не зависит от БД, Kafka и HTTP (проверяет tools/archtest и depguard).
package domain
`},
	{"services/{{name}}/internal/app/doc.go", `// Package app - сценарии сервиса [[.Name]]: транзакции, порты, обработчики сообщений.
// Транзакция бизнес-операции включает изменение агрегата, запись в outbox и аудит; сетевых вызовов внутри нет.
package app
`},
	{"services/{{name}}/internal/adapters/doc.go", `// Package adapters - реализации портов app: PostgreSQL (sqlc), Kafka-обработчики, клиенты смежных систем.
package adapters
`},
}
