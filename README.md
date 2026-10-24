<div align="center">

# Blood Supply

**АС распределения компонентов донорской крови между клиниками: запас против норматива, потребность районов, план пополнения по прогнозу ЧС, заявки врачей и реестр дефицита.**

**Система не ждёт, когда кровь закончится, а заранее распределяет её туда, где она понадобится.**

[Возможности](#возможности) · [Архитектура](#архитектура) · [Быстрый старт](#быстрый-старт) · [Демо за 5 минут](#демо-за-5-минут) · [Документация](#документация) · [План](docs/PLAN.md)

<br />

[![CI](https://img.shields.io/github/actions/workflow/status/AlchemistBro/blood-supply-pet/ci.yml?branch=main&style=for-the-badge&label=CI)](https://github.com/AlchemistBro/blood-supply-pet/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.27-00ADD8?style=for-the-badge&logo=go&logoColor=white)](https://go.dev/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-4169E1?style=for-the-badge&logo=postgresql&logoColor=white)](https://www.postgresql.org/)
[![Kafka](https://img.shields.io/badge/Apache_Kafka-KRaft-231F20?style=for-the-badge&logo=apachekafka&logoColor=white)](https://kafka.apache.org/)
[![Keycloak](https://img.shields.io/badge/Keycloak-OIDC-4D4D4D?style=for-the-badge&logo=keycloak&logoColor=white)](https://www.keycloak.org/)
[![HTMX](https://img.shields.io/badge/HTMX-2-3366CC?style=for-the-badge&logo=htmx&logoColor=white)](https://htmx.org/)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-kind_%2B_kustomize-326CE5?style=for-the-badge&logo=kubernetes&logoColor=white)](https://kubernetes.io/)
[![License: MIT](https://img.shields.io/badge/License-MIT-green?style=for-the-badge)](LICENSE)

</div>

---

> **Статус: проектирование завершено, разработка по этапам.** Текущий этап и прогресс: [docs/PLAN.md](docs/PLAN.md#10-статус-этапов).
> Данные, нормативы и правила подбора в проекте синтетические. Клиническое решение всегда принимает врач.

## О проекте

Запасы компонентов крови распределены по клиникам неравномерно. При чрезвычайной ситуации потребность резко растёт в
конкретных районах, а перераспределение между клиниками идёт вручную, по звонкам. Эта система держит запас каждой клиники
не ниже норматива, заранее пополняет его по агрегированному прогнозу ЧС и подбирает компонент по заявке врача по правилам,
не опуская клинику-источник ниже её защищённого объёма.

Назначение системы — учёт и распределение запаса, планирование и организация передачи между клиниками. Клиническое
решение и индивидуальную совместимость определяет врач: система предлагает варианты по демонстрационным правилам и
объясняет их, но не выбирает за врача и медицинским изделием не задумана. Границы регулирования —
[ADR-0024](docs/adr/0024-regulatory-boundaries.md), применимые акты и обязательства — [регуляторный реестр](docs/compliance/register.md).

Проект вырос из учебного архитектурного проекта (РТУ МИРЭА, дисциплины «Архитектура микросервисов» и DDD): предметная
модель, каталог из 52 функциональных и 17 нефункциональных требований, карта контекстов и сага подтверждения перенесены
как канон, а реализация сделана на Go как production-ready набор микросервисов с Kubernetes.

## Возможности

| | |
| --- | --- |
| **Копия учёта АИСТ-М** | Изменения каждые несколько минут, суточный снимок CSV для сверки, внеочередное обновление оператором; на каждом экране видно, насколько свежи данные |
| **Запас и норматив** | Фактический запас по клинике, виду и группе AB0/Rh; норматив `Kгод/360`, минимум одна единица; подсветка «ниже норматива»; FEFO |
| **Территориальная потребность** | Аналитик загружает CSV (профили районов, история расхода, доли район → клиника); файл проверяется построчно; оценка рассчитывается по истории расхода и долям групп района и публикуется версиями |
| **План по прогнозу ЧС** | Агрегированный прогноз без персональных данных → целевой запас = норматив + фон + доля ЧС → подбор источников, которые никогда не опускаются ниже защищённого объёма → предложения операторам → реестр дефицита |
| **Заявка врача** | Номер случая вместо персональных данных, подтверждённые ограничения, подбор допустимых вариантов по версионируемому справочнику правил, ранжирование с объяснением, выбор врача → задание оператору источника |
| **Сага подтверждения** | Оператор подтверждает единицы: точечная проверка в АИСТ-М, одна транзакция с закреплением (одна единица — одно обязательство, гарантирует уникальный индекс), заказ перевозки через outbox, компенсации при выбытии единицы, замена равноценной, долговечный таймер срока с эскалацией |
| **Логистика без дублей** | Шлюз к перевозчику со сверкой вместо повтора и арендой строки: `kill -9` посреди заказа не создаёт второй машины |
| **Аудит и приватность** | Каждое значимое действие и каждое чтение номера случая в неизменяемом журнале с цепочкой хешей; данных пациентов и пострадавших в системе нет |
| **Интерфейс гос-системы** | Серверный рендеринг на Go + HTMX, вход через OpenID Connect (Keycloak), экраны врача, оператора, аналитика, дежурного; работает без JavaScript |
| **Эксплуатация** | PostgreSQL на сервис, Kafka, OpenTelemetry, Grafana; Kubernetes с CloudNativePG (реплика, PITR), NetworkPolicy по сегментам, учение восстановления |

## Архитектура

Микросервисы по ограниченным контекстам DDD. Между сервисами только события и команды через Kafka (transactional outbox → inbox);
единственный синхронный вызов — проверка статуса единиц перед закреплением. У каждого сервиса своя база.

```mermaid
flowchart LR
    subgraph users[Пользователи]
        U[Врач · Оператор · Аналитик · Дежурный]
    end
    subgraph edge[Сегмент доступа]
        NG[Nginx / ingress]
        KC[Keycloak · OIDC]
    end
    subgraph app[Сервисы Go]
        WEB[web · SSR + HTMX]
        INV[inventory · ФП1]
        DEM[demand · ФП2]
        PLN[planning · ФП3]
        MAT[matching · ФП4]
        TRF[transfer · сага O07]
        LOG[logistics-gateway]
        NTF[notification]
        AUD[audit]
    end
    K{{Kafka}}
    PG[(PostgreSQL · база на сервис)]
    subgraph ext[Смежные системы · имитаторы]
        C01[C01 АИСТ-М]
        C02[C02 Моделирование ЧС]
        C03[C03 Логистика]
        C04[C04 Почта]
    end

    U --> NG --> WEB
    U -. вход .-> KC
    WEB -->|HTTP/JSON · JWT| INV & DEM & PLN & MAT & TRF
    TRF -->|статус единиц · 3 с| INV
    INV & DEM & PLN & MAT & TRF & LOG -->|outbox → события → inbox| K
    K --> NTF & AUD
    app --> PG
    INV <--> C01
    C02 --> PLN
    LOG <--> C03
    NTF --> C04
```

Сага подтверждения передачи:

```mermaid
sequenceDiagram
    actor Op as Оператор источника
    participant T as transfer
    participant I as inventory → АИСТ-М
    participant K as Kafka
    participant L as logistics-gateway → C03
    Op->>T: Подтвердить единицы
    T->>I: статус единиц (синхронно, вне блокировки)
    I-->>T: на месте / выбыли
    Note over T: одна транзакция: блокировка Остатка · защищённый объём ·<br/>CAS PENDING→CONFIRMED · закрепления (UNIQUE unit_id) · outbox
    T-->>Op: «Закреплено, готовьте» в том же запросе
    T->>K: TransferConfirmed · RequestTransport (ключ = O07)
    K->>L: RequestTransport
    L->>L: сверка с C03 вместо повтора, аренда строки
    L->>K: TransportAccepted
    K->>T: inbox · CAS CONFIRMED→TRANSPORT_REQUESTED
```

Подробно: [C4 контекст](docs/architecture/c4-context.md) · [C4 контейнеры](docs/architecture/c4-containers.md) ·
[C4 компоненты transfer и шлюза](docs/architecture/c4-components.md) ·
[карта контекстов](docs/domain/context-map.md) · [состояния O07](docs/domain/obligation-states.md) ·
[сага](docs/architecture/saga-confirmation.md) · [UML: состояния и сверка шлюза](docs/architecture/uml.md) ·
[события и команды](docs/architecture/events.md) ·
[модель данных](docs/architecture/data-model.md) · [развёртывание](docs/architecture/deployment.md) · [ADR](docs/adr/README.md).

Процессы в BPMN 2.0 (открываются в bpmn.io и Camunda Modeler): [подтверждение передачи](docs/architecture/diagrams/bpmn/confirm-transfer.bpmn) ·
[план пополнения по прогнозу ЧС](docs/architecture/diagrams/bpmn/replenishment-plan.bpmn) ·
[заявка врача](docs/architecture/diagrams/bpmn/clinical-request.bpmn) ·
[загрузка CSV и публикация оценки](docs/architecture/diagrams/bpmn/csv-demand.bpmn). Перечень всех схем:
[docs/architecture/diagrams](docs/architecture/diagrams/README.md).

### Стек

**Сервисы:** Go 1.27, `net/http`, OpenAPI + oapi-codegen, pgx + sqlc, goose, franz-go, slog + OpenTelemetry.
**Интерфейс:** `html/template`, HTMX 2, собственный CSS, SSE.
**Платформа:** PostgreSQL 16, Apache Kafka (KRaft), Keycloak, OTel Collector + grafana/otel-lgtm, Mailpit, Nginx.
**Kubernetes:** kind, kustomize, CloudNativePG, ingress-nginx, NetworkPolicy.
**Качество:** testcontainers-go, фитнес-тесты гонок, контрактные тесты JSON Schema, k6, хаос-сценарии, govulncheck, trivy, gitleaks, cosign + SBOM.

## Быстрый старт

Нужны Docker и [go-task](https://taskfile.dev). Go 1.27 нужен только для разработки.

### 1. Клонировать

```bash
git clone https://github.com/AlchemistBro/blood-supply-pet.git
cd blood-supply-pet
```

### 2. Поднять стенд

```bash
task up        # PostgreSQL, Kafka, Keycloak, Grafana, Mailpit, все сервисы и имитаторы
task seed      # 100 клиник, 100 000 единиц, CSV аналитика, правила подбора (демо)
```

### 3. Открыть

| Что | Адрес |
| --- | --- |
| Интерфейс | https://localhost |
| Keycloak | http://localhost:8180 (`admin` / `admin`) |
| Grafana | http://localhost:3000 |
| Почта (Mailpit) | http://localhost:8025 |
| Kafka UI | http://localhost:8090 (`task up -- --profile ui`) |

Демо-учётные записи (пароль `demo`): `physician1`, `operator-c001`, `operator-c002`, `analyst1`, `duty1`, `it1`.

### Разработка

```bash
task dev:transfer         # один сервис локально против платформы из compose
task test                 # юнит-тесты с -race
task test:integration     # на реальных PostgreSQL и Kafka (testcontainers)
task test:fitness         # четыре гонки саги, kill -9 шлюза, дубли доставки
task test:e2e             # сквозные сценарии против поднятого стенда
task ci                   # всё, что делает GitHub Actions
task k8s:up               # kind + kustomize overlay local
```

## Демо за 5 минут

1. Войдите как `analyst1`, загрузите три CSV из `libs/testdata/csv/`, рассчитайте и опубликуйте версию оценки.
2. В dev-панели отправьте прогноз ЧС от имитатора C02. Через несколько секунд в «Плане пополнения» появятся предложения, а непокрытое — в реестре дефицита.
3. Войдите как `operator-c002`: в «Заданиях» задание от плана, срок идёт. Подтвердите две единицы из трёх с флагом «частично» и получите ответ в том же запросе.
4. В dev-панели «выдайте» одну из закреплённых единиц в имитаторе АИСТ-М: задание станет «Нарушено», заказ у перевозчика снят, запись дефицита появилась.
5. Войдите как `physician1`, создайте заявку с ограничением «без замены группы»; выберите удалённый вариант; `operator-c001` получит письмо в Mailpit и задание с таймером.
6. `docker compose kill logistics-gateway` во время подтверждения, затем `docker compose start logistics-gateway`: заказ у C03 ровно один.

## Что показывает проект архитектурно

- Ограниченные контексты и карта контекстов, перенесённые в физические границы сервисов и баз.
- Сага с оркестратором, компенсациями, долговечным таймером и четырьмя задокументированными гонками, покрытыми тестами.
- Transactional outbox, inbox, идемпотентность по ключу, упорядоченность по ключу агрегата, сверка вместо повтора, аренда строки.
- Проекции с водяным знаком вместо распределённых JOIN; честная деградация при задержке.
- Приватность по умолчанию: белый список полей на входе, отсутствие столбцов для ПДн, аудит чтения.
- Схемы C4, UML (sequence, state, class, deployment), BPMN 2.0, ER в репозитории и сверенные с кодом.
- Kubernetes с продовыми практиками: оператор PostgreSQL с PITR, NetworkPolicy по сегментам, probes, HPA, учение восстановления.

## Документация

| Раздел | Где |
| --- | --- |
| План, решения, этапы, статус | [docs/PLAN.md](docs/PLAN.md) |
| Правила разработки | [CONTRIBUTING.md](CONTRIBUTING.md) |
| Канон предметной области: глоссарий, требования, состояния O07, карта контекстов | [docs/domain/](docs/domain/) |
| Архитектура: C4, события, модель данных, сага, развёртывание, безопасность, тесты | [docs/architecture/](docs/architecture/) |
| Решения (ADR) | [docs/adr/](docs/adr/README.md); этап 1: [отступления от канона и прототипа](docs/adr/0027-stage1-deviations.md), [границы регулирования](docs/adr/0024-regulatory-boundaries.md), [перевозка](docs/adr/0022-transport-carrier-and-conditions.md) |
| Регуляторный реестр: акты, статусы, обязательства | [docs/compliance/register.md](docs/compliance/register.md) |
| Задачи по этапам | [docs/tasks/](docs/tasks/) |
| Стиль и экраны интерфейса | [docs/ui/](docs/ui/) |

## Структура проекта

```
libs/         contracts · messaging · httpx · observability · pgx · clock · refdata · testdata
services/     web · inventory · demand · planning · matching · transfer · logistics-gateway · notification · audit · sim-*
deploy/       compose · docker · k8s (base, overlays)
tests/        e2e · load · chaos
docs/         PLAN · domain · architecture · adr · compliance · tasks · ui
```

## Планы

Этапы 0–9 с критериями выхода описаны в [docs/PLAN.md](docs/PLAN.md#4-этапы). Дальше: английский README,
релиз `v1.0.0` с подписанными образами и SBOM, публичный демо-стенд.

## Вклад в проект

Это личный пет-проект, но замечания и вопросы по архитектуре приветствуются через Issues. Перед PR: `task ci`.
Ветка `main` защищена, слияние только через PR с зелёным CI.

## Лицензия

MIT
