# ADR-0012. Набор библиотек и инструментов Go

Статус: принят, 20.10.2026.

## Решение

| Назначение | Выбор | Отклонено | Почему |
| --- | --- | --- | --- |
| Модули | один `go.mod`, `github.com/AlchemistBro/blood-supply-pet` | go.work с модулем на сервис | атомарные изменения контрактов; `internal/` достаточно для границ |
| HTTP-сервер | `net/http` (паттерны маршрутов Go 1.22+) | chi, echo, gin | нет нужды в рамке; меньше зависимостей |
| Контракт HTTP | OpenAPI 3.1 + `oapi-codegen` (std-http server, types) | ручные хендлеры | контракт — источник истины, тесты по нему |
| PostgreSQL | `jackc/pgx/v5` + `sqlc` | GORM, ent, sqlx | явный SQL, типизация, частичные индексы и `FOR UPDATE` без магии |
| Миграции | `pressly/goose/v3`, SQL-файлы, embed | atlas, tern | просто, встроено в бинарник |
| Kafka | `twmb/franz-go` | segmentio/kafka-go, confluent | идемпотентный продюсер, производительность, чистый Go |
| OIDC | `coreos/go-oidc/v3`, `golang.org/x/oauth2` | самописное | стандарт |
| Cookie-сессии | `gorilla/securecookie` | redis-сессии | один `web`, без состояния на сервере |
| Шаблоны | `html/template` + `embed`; HTMX 2.x вендорится | templ, React | без генерации и Node |
| Логи и телеметрия | `log/slog` (JSON), `go.opentelemetry.io/otel` (trace, metric, log), `otelhttp`, `otelpgx`, kafka-заголовки | zap, zerolog, prometheus client напрямую | один стандарт, OTLP в lgtm |
| Конфиг | `caarlos0/env/v11` | viper | только переменные окружения, 12-factor |
| Устойчивость | `sony/gobreaker/v2`, `cenkalti/backoff/v5` | самописное | предохранитель и backoff с jitter |
| UUID | `google/uuid` (v7) | — | — |
| Decimal | не нужен: объёмы в мл `int64`, доли `float64` с округлением до 4 знаков | shopspring/decimal | как в ПР6 (`numeric(5,4)`) |
| Тесты | `testing`, `testcontainers-go` (postgres, kafka modules), `pgregory.net/rapid`, `golang/mock` не используется (ручные заглушки) | testify | меньше магии; свойственные тесты для правил плана |
| Линт | `golangci-lint` v2 (govet, staticcheck, errcheck, gosec, revive, depguard, exhaustive, gocritic) | — | depguard запрещает прямой Kafka вне `libs/messaging` |
| Задачи | `go-task` (Taskfile.yml) | Makefile | Windows-дружелюбно |
| Образы | многостадийный Dockerfile, `gcr.io/distroless/static` | ko | единый путь для всех сервисов |
| Нагрузка | k6, `testing.B` | vegeta | скрипты сценариев ролей |
| Схемы | Mermaid в Markdown (C4, sequence, state, ER), BPMN 2.0 XML + SVG (bpmn.io совместимый), экспорт SVG через `mmdc` в `docs:diagrams` | draw.io | рендерится на GitHub, diff-able |

Версии фиксируются в `go.mod`, обновляются dependabot-ом, мажорные обновления — отдельным PR.
