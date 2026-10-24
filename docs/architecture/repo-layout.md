# Структура репозитория и анатомия сервиса

```
blood-supply-pet/
├── README.md · CONTRIBUTING.md · LICENSE · Taskfile.yml · go.mod · .golangci.yml · .editorconfig
├── .github/workflows/        ci.yml · e2e.yml · k8s.yml · images.yml · load.yml · docs.yml
├── docs/
│   ├── PLAN.md               план, решения, этапы, статус
│   ├── adr/                  ADR-0001…
│   ├── domain/               glossary · requirements · obligation-states · context-map
│   ├── architecture/         c4-context · c4-containers · c4-components · uml · events · data-model · saga-confirmation · deployment · security · testing · repo-layout
│   │   └── diagrams/         README (перечень схем) · bpmn/*.bpmn + svg · c4/*.svg (экспорт) · screenshots/
│   ├── tasks/                00-foundation … 09-hardening
│   ├── ui/                   style-guide · screens
│   └── perf/                 отчёты нагрузки
├── libs/
│   ├── contracts/            Go-типы событий и команд, schemas/*.json, examples/, openapi/ (общие компоненты)
│   ├── messaging/            outbox, relay, inbox, consumer, headers, dlt
│   ├── httpx/                сервер, middleware (auth JWT, correlation id, recover, logging), problem+json, клиент с retry/breaker
│   ├── observability/        slog + OTel инициализация, метрики RED, маскирование
│   ├── pgx/                  пул, транзакции, advisory lock, goose runner
│   ├── clock/                Clock интерфейс и MutableClock для тестов
│   ├── refdata/              справочники (клиники, районы, виды, группы, срочность) + сид-миграция
│   └── testdata/             генератор синтетических данных
├── services/
│   ├── web/                  BFF + SSR UI
│   ├── inventory/ demand/ planning/ matching/ transfer/ logistics-gateway/ notification/ audit/
│   └── sim-aist/ sim-emergency/ sim-logistics/
├── tests/
│   ├── e2e/                  сквозные сценарии против compose
│   ├── load/                 k6 скрипты
│   └── chaos/                сценарии для kind
├── tools/
│   ├── archtest/             проверка правил импорта
│   └── bpmn-validate/        валидация BPMN XML
└── deploy/
    ├── compose/              compose.yaml · compose.dev.yaml · postgres/ · keycloak/ · nginx/ · otel/ · .env.example
    ├── docker/Dockerfile     один многостадийный, ARG SERVICE
    └── k8s/                  base/ · overlays/{local,ci,prod-example} · drill/
```

## Анатомия сервиса (пример `services/transfer`)

```
services/transfer/
├── cmd/transfer/main.go          сборка зависимостей, запуск HTTP, consumer, relay, таймер; graceful shutdown
├── api/openapi.yaml              контракт HTTP (источник истины), генерируется сервер в internal/api/gen
├── migrations/                   0001_transfer_core.sql · 0002_messaging.sql … (goose, embed)
├── sqlc/                         sqlc.yaml · queries/*.sql → internal/adapters/postgres/gen
└── internal/
    ├── domain/                   Obligation, Allocation, SourceStock, ConfirmationResult, правила переходов; без зависимостей
    ├── app/                      TransferService: Open, Confirm, Replace, Cancel, ExpireOverdue, OnLogisticsReply, OnUnitEvent; порты (интерфейсы)
    ├── adapters/
    │   ├── postgres/             репозитории через sqlc, транзакции, блокировки
    │   ├── kafka/                consumer-обработчики → app; publisher = libs/messaging.Journal
    │   ├── inventoryclient/      HTTP-клиент статуса единиц с таймаутом и предохранителем (ADR-0004)
    │   └── http/                 хендлеры OpenAPI → app, problem+json
    └── fitness/                  тесты гонок (integration)
```

Зависимости направлены внутрь: `adapters → app → domain`. `cmd` знает всё. Ничто в `internal/` не импортирует другой сервис.

## Taskfile (ключевые задачи)

`up`, `down`, `dev:<svc>`, `gen` (oapi-codegen, sqlc), `fmt`, `lint`, `test`, `test:integration`, `test:fitness`, `test:e2e`,
`contracts:check`, `bench:plan`, `build`, `images`, `ci`, `k8s:up`, `k8s:down`, `k8s:smoke`, `k8s:drill`, `docs:diagrams`
(рендер Mermaid в SVG и BPMN в SVG для README), `seed` (загрузить демо-данные в поднятый стенд).
