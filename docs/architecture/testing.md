# Стратегия тестирования и CI/CD

## Пирамида

| Уровень | Где | Инструменты | Запуск |
| --- | --- | --- | --- |
| Юнит домена | `services/*/internal/domain` | `testing`, табличные тесты, `testing/quick` или `pgregory.net/rapid` для свойств (NFR013, NFR014, подбор) | `task test` (каждый PR, `-race`) |
| Юнит приложения | `internal/app` с портами-заглушками | `testing` | `task test` |
| Интеграционные | `internal/adapters`, `internal/app` с реальными PostgreSQL и Kafka | `testcontainers-go`, `//go:build integration` | `task test:integration` (каждый PR) |
| Фитнес-тесты саги | `services/transfer`, `services/logistics-gateway` | Перенос из Java: четыре гонки, 50 параллельных подтверждений, восемь экземпляров шлюза, kill после REQUESTING, потерянный ответ | `task test:fitness` (каждый PR, обязательные) |
| Контрактные | `libs/contracts` | JSON Schema валидация примеров, diff схем на совместимость, OpenAPI lint (`vacuum`/`spectral`), `oapi-codegen` diff | `task contracts:check` |
| Архитектурные | `tools/archtest` | запрет импорта `services/*` из `libs`, запрет прямого `kgo.Produce` вне `libs/messaging`, запрет `database/sql` вне adapters | `task lint` |
| Авторизация | каждый HTTP-эндпоинт | таблица роль × эндпоинт → 2xx/403 из `tools/authmatrix` (раздел «Матрица прав») | `task authmatrix`, `task test:integration` |
| E2E smoke | `tests/e2e` | Go-тесты против поднятого compose: сквозной сценарий из защиты, 4 сценария саги, `docker compose kill logistics-gateway` | `task test:e2e` (PR с меткой `e2e`, main, nightly) |
| Нагрузка | `tests/load` | k6: NFR003 p95 ≤ 2 с на экранах запаса, плана, вариантов; Go-бенчмарк NFR002 | nightly, порог в CI |
| Хаос | `tests/chaos` | kill pod transfer/gateway в kind, пауза Kafka, недоступность sim-aist и sim-logistics | nightly на kind |
| Восстановление | `deploy/k8s/drill` | PITR CloudNativePG, измерение RTO/RPO | ежемесячно вручную + nightly ускоренный |
| Безопасность | CI | `govulncheck`, `gosec`, `trivy image`, `gitleaks`, `kubeconform`, `kube-linter` | каждый PR |
| UI | `services/web` | golden-тесты шаблонов, `httptest` формы без JS, Playwright (3 сценария ролей, скриншоты для README) | PR / nightly |

Правило «тест действительно проверяет»: перед закрытием этапа разработчик отключает ключевое правило (уникальный индекс,
сверку, аренду, перепроверку копии, белый список) и фиксирует в задаче этапа, какой тест упал. Так делалось в Java-прототипе
(без сверки падают три теста, без аренды — тест двух экземпляров).

## Матрица прав

Тест «роль × эндпоинт» строится автоматически (NFR011), чтобы новый эндпоинт не обошёлся без теста прав.

Соглашение для `services/*/api/openapi.yaml`: каждая операция (метод пути) несёт ровно одну метку.

- `x-bs-operation: <Operation>` — имя операции из матрицы `libs/httpx/auth/policy.go`, например `READ_AUDIT`
  или `INTERNAL_UNIT_STATUS`. Хендлер проверяет права по этой же операции (`auth.Require`).
- `x-bs-public: true` — эндпоинт без проверки прав: `/healthz`, `/readyz`, `/metrics`, `/openapi.yaml` и dev-маршруты
  имитаторов, если они описаны в контракте.

`tools/authmatrix` читает контракты всех сервисов и проверяет: у каждой операции есть одна из меток, операция есть в
матрице, каждая роль из `policy.go` разрешена хотя бы для одной операции. По контрактам и матрице он строит таблицу
`docs/architecture/auth-matrix.md` (2xx — допуск, 403 — отказ):

```
go run ./tools/authmatrix -check                                  # task authmatrix, входит в task ci
go run ./tools/authmatrix -out docs/architecture/auth-matrix.md    # после изменения контракта или матрицы
```

Таблица — генерируемый файл: устаревшая таблица роняет `-check` и тест `tools/authmatrix/matrix`.
Сервисные тесты перебирают роли пакетом `tools/authmatrix/matrix`: `matrix.Cases("<service>")` (или
`matrix.CasesFromSpec` по встроенному `apispec.Spec`) возвращает `[]Case{Method, Path, Role, WantForbidden}`
для всех ролей и всех защищённых операций; путь заполняется `matrix.FillPath`, токен роли выпускает
`libs/httpx/auth/authtest`. `WantForbidden` → ожидается 403, иначе ответ не 401 и не 403.

## Проверки без Docker

`go test -count=1 -p 2 -parallel 4 ./... ./libs/testdata` запускает обычные тесты без тегов
`integration` и `e2e`. В них используются локальные HTTP/SMTP-имитаторы, контейнеры не нужны.

Для PostgreSQL задать административный URL отдельного тестового сервера в `BS_TEST_DB_URL` и выполнить
`task test:postgres`. Обвязка создаёт базу `bs_test_<случайный UUID>` на каждую проверку и удаляет именно
эту базу после закрытия пулов. Роль должна иметь право создавать базы; база из строки подключения
не используется для таблиц приложения. Строка подключения и пароль в диагностике обвязки не выводятся.

`BS_TEST_NO_DOCKER=true` запрещает неявный запуск Testcontainers: отсутствие URL PostgreSQL — ошибка,
а проверки Kafka без `BS_TEST_KAFKA_BROKERS` явно пропускаются. Пропуск не подтверждает обмен через Kafka.
Развёртывание compose/kind, e2e, нагрузка и сценарии отказов — отдельные задачи; эта команда их не вызывает.

На Windows URL можно прочитать из файла окружения без вывода секрета:

```powershell
powershell -NoProfile -File tools/test-postgres.ps1 -EnvFile .env
```

Скрипт читает `DATABASE_PUBLIC_URL`, затем `BS_TEST_DB_URL` или `DATABASE_URL`; файл не исполняется как код.
Поддерживается явный путь к файлу, список пакетов через `-Packages` и фильтр имён тестов `-Run`.
Кодировка вывода и переменные окружения восстанавливаются после прогона.

## Тестовые данные

`libs/testdata` генерирует синтетические данные (ADR-0010): 100 клиник в 12 районах, 4 вида компонентов, 8 групп,
до 100 000 единиц с реалистичным распределением сроков годности; CSV-файлы для demand; прогноз ЧС на 2 района;
10 правил O15 с пометкой «демо». Один сид → детерминированный набор.

## GitHub Actions

| Workflow | Триггер | Шаги |
| --- | --- | --- |
| `ci.yml` | PR, push в `main` | checkout → setup-go (кэш) → `task gen` + проверка чистого git diff → `task lint` → `task test` → `task test:integration` (docker доступен) → `task test:fitness` → `task contracts:check` → `task build` → `trivy fs`, `govulncheck`, `gosec`, `gitleaks` → `kubeconform` на `kustomize build` |
| `e2e.yml` | push в `main`, метка `e2e`, nightly | `task up` → `task test:e2e` → `task down`; артефакты: логи compose, HAR, скриншоты Playwright |
| `k8s.yml` | push в `main`, nightly | kind create → `task k8s:up` (overlay ci) → smoke Job → chaos nightly → `task k8s:drill` (ускоренный) |
| `images.yml` | push в `main`, теги `v*` | `docker buildx` на все сервисы → push в ghcr.io (`edge` / `vX.Y.Z` / `sha`) → cosign sign → syft SBOM attestation |
| `load.yml` | nightly | `task up` → k6 → порог p95; `task bench:plan` → порог 30 с; отчёт в артефакты и в `docs/perf/latest.md` |
| `docs.yml` | PR | markdownlint, проверка Mermaid (`mmdc` рендер), проверка ссылок, BPMN XML валидация |
| `dependabot.yml` | еженедельно | Go modules, GitHub Actions, Docker base images |

Ветки: `main` защищена, PR с зелёным `ci.yml` обязателен. Релиз: тег `vX.Y.Z` → `images.yml` + GitHub Release с changelog (git-cliff).

## Определение готовности (Definition of Done) для пункта чеклиста

1. Тест написан до кода и был красным.
2. `task fmt lint test` зелёные локально; для адаптеров — `task test:integration`.
3. Контракт (OpenAPI/JSON Schema) обновлён и прогнан `task contracts:check`.
4. Аудит и события записаны в транзакции; логи без ПДн.
5. Документация: затронутые `docs/architecture/*.md` и схемы обновлены в том же PR.
6. Пункт отмечен в `docs/tasks/NN-*.md` с ссылкой на коммит.
