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

## Статус выполнения (20.10.2026)

Записано по результатам запусков, а не по намерениям.

**Сделано и проверено запуском**
- Модуль Go, Taskfile (`task --list` работает), `.golangci.yml`: линтер показывает 0 замечаний, `go vet` чист в том числе с тегами `integration` и `e2e`.
- Библиотеки `clock`, `correlation`, `pgx`, `contracts`, `httpx` (с JWT и матрицей прав), `observability`, `refdata`, `testdata`, `runtime`: юнит-тесты зелёные.
- `tools/archtest` проверяет границы зависимостей, тест контрактов сверяет примеры, JSON Schema и Go-типы.
- `libs/messaging` на настоящих PostgreSQL и Kafka (Testcontainers): четыре теста зелёные за 9,5 с. Это кругооборот outbox → Kafka → inbox с порядком, падение ретранслятора между отправкой и отметкой с отсечением дубля inbox, остановка на первой ошибке, ядовитое сообщение в DLT.
- `libs/refdata`: сид справочников идемпотентен (интеграционный тест на PostgreSQL).
- Сервис `audit`: цепочка хешей (юнит-тесты на подмену и удаление записи), миграция с триггером запрета UPDATE и DELETE, контракт OpenAPI, сгенерированный сервер и SQL.
- Четыре workflow GitHub Actions проходят `actionlint` без замечаний; `docker compose config` валиден.

**Проверка «тест действительно проверяет» (выполнена)**
- Inbox отключён (`FirstTime` всегда true): падают `TestOutboxRelayInboxRoundTrip` и `TestRelayCrashBetweenSendAndMarkIsDeduplicatedByInbox`.
- Ретранслятор не останавливается на ошибке: падает `TestRelayStopsOnFirstErrorAndKeepsOrder`.
- Правила возвращены, все четыре теста снова зелёные.

**Написано, но ещё не запускалось (нужен стабильный Docker)**
- Интеграционные тесты сервиса audit: параллельные записи, дубль доставки, ядовитые сообщения, запрет изменения журнала, обнаружение подмены. Проверка «тест проверяет» для них: убрать `FOR UPDATE` с головы цепочки (должен упасть тест параллельных записей).
- Стенд `task up`: импорт realm Keycloak и содержимое токенов (роль, `clinic_id`, `aud`), nginx с TLS, сервис audit в контейнере.
- Сквозные тесты `tests/e2e` (токены, права по ролям, событие через Kafka в журнал, дубль, запись от служебного клиента).
- `task audit:verify` против поднятого стенда.

**Не сделано**
- Шаблон сервиса `tools/scaffold` и задача `task docs:diagrams` (рендер Mermaid в SVG).
- Детектор гонок `-race` на этой машине: нет gcc. В CI он включён.
- Запуск workflow в GitHub Actions: изменения ещё не отправлены в репозиторий.
- Защита ветки `main` в GitHub: настраивается владельцем вручную.

**Причина задержки.** Docker Desktop на этой машине останавливал движок по простою, а VPN Outline обрывался и сбрасывал DNS; это ломало загрузку образов и связь агента с API. Разбор и меры: [docs/runbooks/dev-environment-windows.md](../runbooks/dev-environment-windows.md).

## Критерий выхода
`task up` → все контейнеры healthy за ≤ 2 мин; `task ci` зелёный локально и в GitHub Actions; тест outbox → Kafka → inbox
проходит, включая сценарий «relay убит посреди пачки, перезапущен, дублей у получателя нет».

## Проверка «тест действительно проверяет»
Убрать `FirstTime` из обработчика → тест дубля доставки должен упасть. Убрать остановку relay на первой ошибке → тест порядка должен упасть.

## Открытые вопросы
Нет.
