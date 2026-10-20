# Этап 0. Фундамент

Цель: монорепозиторий, в котором любой следующий сервис добавляется за час по шаблону, а платформа поднимается одной командой.
Требования: инфраструктурные (NFR008 каркас аудита, NFR011 каркас JWT). Срок-ориентир: 1–2 недели.

## Чеклист

### Репозиторий и инструменты
- [ ] `go.mod` `github.com/AlchemistBro/blood-supply-pet`, Go 1.27; `.editorconfig`; `.golangci.yml` по ADR-0012 (включая depguard: `kgo` только в `libs/messaging`).
- [ ] `Taskfile.yml` с задачами из `docs/architecture/repo-layout.md`; все задачи работают в Git Bash на Windows и в Linux CI.
- [ ] `tools/archtest`: тест, что `libs/**` не импортирует `services/**`; что `services/<a>/internal` не упоминается вне `services/<a>` (страховка к компилятору); что `internal/domain` не импортирует pgx/kafka/http.
- [ ] Шаблон сервиса `tools/scaffold` (генерация `services/<name>` по анатомии из `repo-layout.md`): `task scaffold -- name=demo`.

### Платформа (compose)
- [ ] `deploy/compose/compose.yaml` и `compose.dev.yaml`: postgres (init: роли и базы на 8 сервисов), kafka KRaft, keycloak с импортом realm `blood-supply` (роли, атрибут `clinic_id`, демо-пользователи), otel-collector + `grafana/otel-lgtm`, mailpit, nginx с самоподписанным TLS, kafka-ui (профиль `ui`). Healthcheck у всех.
- [ ] `deploy/compose/.env.example`; `task up` / `task down` / `task logs`.
- [ ] `deploy/docker/Dockerfile` многостадийный с `ARG SERVICE`, distroless, non-root; `task images` собирает все сервисы.

### Библиотеки
- [ ] `libs/clock`: `Clock`, `MutableClock`.
- [ ] `libs/pgx`: пул из `BS_DB_URL`, `WithTx`, advisory lock, запуск goose-миграций из embed при старте.
- [ ] `libs/messaging`: `Journal` (запись outbox/аудита в tx), `Relay` (порядок по id, остановка на первой ошибке, один активный через advisory lock, метрики lag), `Inbox.FirstTime`, `Consumer` (ручной коммит, retry с backoff, DLT), заголовки по `events.md`, тест outbox → Kafka → inbox на Testcontainers (включая дубль доставки и падение relay посреди пачки).
- [ ] `libs/contracts`: каркас: пакеты по топикам, `schemas/`, `examples/`, `task contracts:check` (валидация примеров, diff схем). Первые контракты: `AuditRecorded`, заголовки.
- [ ] `libs/httpx`: сервер с graceful shutdown, `/healthz`, `/readyz`, `/metrics`, middleware correlation id, recover, slog access log, problem+json ошибки; `auth`: проверка JWT по JWKS с кэшем, извлечение роли и `clinic_id`, матрица политик `policy.go`, тестовый JWKS для интеграционных тестов; HTTP-клиент с таймаутом, retry, предохранителем.
- [ ] `libs/observability`: инициализация slog JSON + OTel (OTLP), атрибуты сервиса/версии, маскирование `patient_case_no` на экспорте.
- [ ] `libs/refdata`: JSON справочников (100 клиник в 12 районах, виды RBC/FFP/PLT/CRYO, 8 групп, срочность PLANNED/URGENT/EMERGENCY с окнами 24 ч / 2 ч / 15 мин), Go-типы, сид-миграция `ref_*`.
- [ ] `libs/testdata`: детерминированный генератор единиц, годового расхода, CSV, прогноза; `task seed`.

### Пример и сквозная проверка
- [ ] Сервис-пример `services/_template` (или первый реальный `audit` в минимальном виде): HTTP + миграции + consumer `audit.events` + запись в таблицу. Удаляется или становится `audit` на этапе 7.
- [ ] `tests/e2e/platform_test.go`: compose поднят → Keycloak выдаёт токен демо-пользователю → сервис принимает JWT → событие проходит через Kafka в `audit`.

### CI/CD
- [ ] `.github/workflows/ci.yml` по `testing.md` (gen diff, lint, test -race, integration, contracts, build, trivy fs, govulncheck, gosec, gitleaks, kubeconform-заглушка).
- [ ] `.github/workflows/images.yml`: сборка и push в ghcr.io с тегом `edge`, cosign, SBOM (включается, когда есть первый сервис).
- [ ] `dependabot.yml`; защита ветки `main` (настроить в GitHub вручную — записать инструкцию в README раздел «Вклад»).
- [ ] Бейджи CI в README живые.

### Документация
- [ ] `docs/architecture/diagrams/bpmn/README.md`: как редактировать BPMN (bpmn.io / Camunda Modeler), конвенции пулов и дорожек.
- [ ] `task docs:diagrams` рендерит Mermaid из `docs/**/*.md` в `docs/architecture/diagrams/c4/*.svg` (mmdc в Docker).

## Критерий выхода
`task up` → все контейнеры healthy за ≤ 2 мин; `task ci` зелёный локально и в GitHub Actions; тест outbox → Kafka → inbox
проходит, включая сценарий «relay убит посреди пачки, перезапущен, дублей у получателя нет».

## Проверка «тест действительно проверяет»
Убрать `FirstTime` из обработчика → тест дубля доставки должен упасть. Убрать остановку relay на первой ошибке → тест порядка должен упасть.

## Открытые вопросы
Нет.
