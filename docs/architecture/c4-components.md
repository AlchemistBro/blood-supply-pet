# C4 · Уровень 3: компоненты `transfer` и `logistics-gateway`

Два сервиса, на которых держится сага подтверждения. Компоненты соответствуют пакетам сервиса
([repo-layout.md](repo-layout.md), раздел «Анатомия сервиса»): зависимости направлены внутрь,
`adapters → app → domain`; общие механизмы — платформенные библиотеки `libs/`. Поведение — [сага](saga-confirmation.md),
[состояния O07](../domain/obligation-states.md), BPMN [confirm-transfer.bpmn](diagrams/bpmn/confirm-transfer.bpmn).

## transfer

```mermaid
C4Component
    title transfer · компоненты (оркестратор саги подтверждения)

    Container(web, "web", "Go, SSR + HTMX", "BFF: операции оператора и дежурного с JWT пользователя")
    Container(inventory, "inventory", "Go", "Копия учёта АИСТ-М, точечный статус единиц")
    ContainerQueue(kafka, "Kafka", "KRaft", "transfer.commands, inventory.events, logistics.replies → transfer, transfer.events, logistics.commands, inventory.commands, audit.events ← transfer")
    ContainerDb(db, "БД transfer", "PostgreSQL 16", "obligation, allocation, source_stock, unit_copy, outbox, inbox, audit_local")

    Container_Boundary(trf, "transfer") {
        Component(api, "HTTP API", "net/http, oapi-codegen, libs/httpx", "Open, Confirm, Replace, Cancel, чтение O07, проверка JWT и роли, problem+json")
        Component(consumers, "Обработчики сообщений", "adapters/kafka, libs/messaging Consumer", "transfer.commands, inventory.events, logistics.replies, ручной коммит после TX, повторы, DLT")
        Component(timer, "Таймер срока", "воркер, опрос 5 с", "ExpireOverdue: просроченные PENDING → EXPIRED, эскалация к следующему источнику")
        Component(app, "TransferService", "internal/app", "Сценарии саги: одна TX = агрегат + outbox + аудит, порядок блокировок Остаток → O07, сетевых вызовов в TX нет")
        Component(domain, "Домен", "internal/domain", "Obligation и CAS-переходы, Allocation, SourceStock, ConfirmationResult, ReplacementResult, спецификация замены, без pgx, Kafka, HTTP")
        Component(store, "Хранилище", "adapters/postgres, pgx + sqlc", "CAS UPDATE … WHERE status, SELECT … FOR UPDATE, UNIQUE (unit_id) WHERE CONFIRMED, запрет DELETE allocation")
        Component(invclient, "inventoryclient", "HTTP-клиент", "GET /internal/units/status: таймаут 3 с, предохранитель, сервисный токен (ADR-0004)")
        Component(journal, "Journal (outbox)", "libs/messaging", "События, команды и аудит в outbox той же транзакцией")
        Component(relay, "Ретранслятор outbox", "libs/messaging Relay", "outbox → Kafka по порядку id, остановка на первой ошибке, один активный через advisory lock")
    }

    Rel(web, api, "HTTP/JSON", "JWT")
    Rel(api, app, "вызывает")
    Rel(kafka, consumers, "сообщения", "franz-go")
    Rel(consumers, app, "OnCommand, OnUnitEvent, OnLogisticsReply")
    Rel(timer, app, "ExpireOverdue")
    Rel(app, domain, "правила переходов")
    Rel(app, invclient, "CheckUnits до транзакции")
    Rel(invclient, inventory, "GET /internal/units/status", "HTTP, 3 с")
    Rel(app, store, "TX, блокировки, CAS")
    Rel(app, journal, "outbox в TX")
    Rel(store, db, "SQL", "pgx")
    Rel(journal, db, "INSERT outbox")
    Rel(relay, db, "читает outbox")
    Rel(relay, kafka, "публикует", "идемпотентный продюсер, acks=all")

    UpdateLayoutConfig($c4ShapeInRow="3", $c4BoundaryInRow="1")
```

| Компонент | Пакет | Отвечает за | Требования, ADR |
| --- | --- | --- | --- |
| HTTP API | `internal/api`, `internal/adapters/http` | Контракт `api/openapi.yaml`; роль и клиника из токена; ответ подтверждения в том же запросе | FR028, FR043, FR048, NFR011, ADR-0015 |
| Обработчики сообщений | `internal/adapters/kafka` | Inbox в транзакции обработчика; `OpenObligation`, `CancelObligation`; проекция `unit_copy` и компенсации; ответы шлюза | ADR-0003, ADR-0027 |
| Таймер срока | `internal/app` (воркер) | Частичный индекс `(deadline) WHERE status = 'PENDING'`; новое O07 с `choice_id = uuid v5` | FR049, ADR-0027 |
| TransferService | `internal/app` | `Open`, `Confirm`, `Replace`, `Cancel`, `ExpireOverdue`, `OnUnitEvent`, `OnLogisticsReply`; порты `Store`, `UnitStatusChecker` | FR048, FR049, NFR004, NFR013 |
| Домен | `internal/domain` | Статусы и переходы O07, результаты подтверждения и замены, спецификация замены | `obligation-states.md`, ADR-0011 |
| Хранилище | `internal/adapters/postgres` | Таблицы `obligation`, `allocation`, `source_stock`, `unit_copy` | NFR004, ПП РФ № 641 п. 17–18 |
| inventoryclient | `internal/adapters/inventoryclient` | Единственный синхронный межсервисный вызов; `SourceSystemUnavailable` при сбое | ADR-0004, ADR-0027 |
| Journal, ретранслятор | `libs/messaging` | Transactional outbox, порядок по ключу `obligation_id` | ADR-0003 |

## planning

```mermaid
flowchart LR
    WEB[web BFF] --> API[OpenAPI и проверка роли]
    C02[C02: агрегированный прогноз] --> API
    API --> APP[app.Service: прогноз, коррекция, план, дефицит]
    KIN[Kafka: inventory, demand, logistics, transfer, matching] --> PROJ[Handle: inbox и версии проекций]
    PROJ --> DB[(planning: проекции и журнал снимков)]
    APP --> INPUT[Снимок версий входов]
    INPUT --> DB
    APP --> DOMAIN[Чистый domain: распределение ЧС, защищённый объём, источники]
    DOMAIN --> APP
    APP --> DB
    APP --> JOURNAL[Journal: команды, события и аудит в outbox]
    JOURNAL --> DB
    DB --> RELAY[Outbox relay]
    RELAY --> KOUT[Kafka: OpenObligation, защищённый объём, дефицит]
    CLI[planning-rebuild] --> APP
```

Расчёт сохраняет входы и результат одной транзакцией. Фон приводится к периоду прогноза (ADR-0029),
защищённые пороги включаются в команду (ADR-0028). Пересчёт отменяет только прежние PENDING через
надгробие запроса (ADR-0030). Восстановление проекций использует локальные снимки и не повторяет задания (ADR-0034).

## matching

```mermaid
flowchart LR
    WEB[web BFF и JWT пользователя] --> API[OpenAPI: роль, клиника, автор заявки]
    API --> APP[app.Service: заявка, варианты, выбор, отмена]
    APP --> DOMAIN[Чистый domain.Match: правила O15 и ранжирование]
    APP --> K1[k1seal: номер случая и AAD заявки]
    K1 --> DB[(matching: заявка, шифротекст, варианты и выбор)]
    APP --> DB
    KIN[Kafka: запас, нормы, логистика и O07] --> PROJ[inbox и собственные проекции]
    PROJ --> DB
    PROJ --> FOLLOW[Повтор по непокрытому остатку]
    FOLLOW --> APP
    API --> READ[PatientCase: проверка области доступа]
    READ --> AUDIT[Аудит чтения до выдачи номера]
    AUDIT --> DB
    READ --> K1
    APP --> JOURNAL[Journal и outbox]
    JOURNAL --> DB
    DB --> RELAY[Outbox relay]
    RELAY --> KOUT[Kafka: OpenObligation, отмена, дефицит и аудит]
```

Варианты не содержат номера случая или фенотипа. Номер открывается отдельной операцией только автору-врачу
или оператору источника активной подтверждённой передачи; отказ аудита закрывает выдачу (ADR-0020).
ИНТ-05 выполняет inventory через постоянную очередь; matching читает квитанции, не вызывает его HTTP (ADR-0031).

## logistics-gateway

```mermaid
C4Component
    title logistics-gateway · компоненты (ACL к службе перевозки C03)

    ContainerQueue(kafka, "Kafka", "KRaft", "logistics.commands → шлюз, logistics.replies, logistics.events, audit.events ← шлюз")
    ContainerDb(db, "БД logistics", "PostgreSQL 16", "transport_request (аренда, попытки, надгробие), delivery_estimate, outbox, inbox, audit_local")
    System_Ext(c03, "C03 служба перевозки", "Заказы перевозки, оценки времени, не дедуплицирует заказы")

    Container_Boundary(lg, "logistics-gateway") {
        Component(consumer, "Обработчик команд", "adapters/kafka, libs/messaging Consumer", "RequestTransport → AcceptRequest, CancelTransport → AcceptCancel")
        Component(gateway, "Gateway", "internal/app", "Inbox + строка REQUESTING одной TX, Drive: аренда → сверка по clientRef → заказ, исход и ответ в outbox, пока аренда наша")
        Component(reconciler, "Сверщик", "internal/app, фоновый воркер", "ReconcileDue: строки с истёкшей арендой, просроченной попыткой, UNKNOWN, тот же Drive")
        Component(gdomain, "Домен", "internal/domain", "Статусы REQUESTING/ACCEPTED/REJECTED/UNKNOWN/FAILED/CANCELLED, решение после сверки, политика повторов")
        Component(gstore, "Хранилище", "adapters/postgres, pgx + sqlc", "PK obligation_id, Claim (аренда), Finish и FailAttempt с CAS по lease_owner, надгробие")
        Component(carrier, "Клиент C03", "adapters/c03, HTTP", "FindActive(clientRef), Submit, Cancel, сбой или таймаут → ErrCarrierUnavailable")
        Component(estimates, "Оценки времени", "запланировано, ADR-0014", "Матрица «источник → получатель» из C03 → DeliveryEstimatesPublished")
        Component(gjournal, "Journal и ретранслятор", "libs/messaging", "Ответы TransportAccepted, TransportRejected, TransportCancelled в outbox, relay в Kafka")
    }

    Rel(kafka, consumer, "logistics.commands")
    Rel(consumer, gateway, "AcceptRequest, AcceptCancel")
    Rel(reconciler, gateway, "Drive по просроченным строкам")
    Rel(gateway, gdomain, "Begin, AfterReconcile, OnCarrierUnavailable")
    Rel(gateway, gstore, "аренда, исход, попытки")
    Rel(gateway, carrier, "сверка раньше заказа, вне транзакции")
    Rel(gateway, gjournal, "ответ в outbox в TX исхода")
    Rel(carrier, c03, "ИНТ-03b", "HTTPS/JSON, clientRef = o07-…")
    Rel(estimates, c03, "ИНТ-03a", "HTTPS/JSON")
    Rel(estimates, gjournal, "logistics.events")
    Rel(gstore, db, "SQL", "pgx")
    Rel(gjournal, db, "outbox")
    Rel(gjournal, kafka, "logistics.replies, logistics.events")

    UpdateLayoutConfig($c4ShapeInRow="3", $c4BoundaryInRow="1")
```

| Компонент | Отвечает за | Почему так |
| --- | --- | --- |
| Gateway | Ровно один заказ у C03 на обязательство | Перевозчик не дедуплицирует: повтор вслепую после потерянного ответа создал бы вторую машину. Сверка по `clientRef` всегда раньше отправки |
| Аренда строки (`lease_owner`, `lease_until`, 30 с) | С C03 по строке говорит один экземпляр | `kill -9` посреди заказа: аренда истекает, сверщик продолжает; несколько экземпляров шлюза не создают второй заказ |
| CAS по `lease_owner` при записи исхода | Исход опоздавшего владельца отбрасывается | Ответ transfer уходит только от того, кто держал аренду |
| `UNKNOWN` и `FAILED` | Честное «не знаем» после `max-attempts` (5) | transfer получает `TransportRejected(OUTCOME_UNKNOWN)` и эскалирует дежурному; сверка продолжается, найденный заказ даёт запоздалый `TransportAccepted`, не найденный — `FAILED` без повторной отправки |
| Надгробие | `CancelTransport` обогнал `RequestTransport` | Строка `CANCELLED` без маршрута; поздняя команда упирается в первичный ключ, заказа нет |
| Оценки времени | Расчёт плана и подбор без синхронных вызовов | ADR-0014; реализация вне этапа 1 |

Состояния строки `transport_request` — [uml.md](uml.md#состояния-заказа-перевозки-transport_request),
сверка при потерянном ответе — [uml.md](uml.md#сверка-шлюза-при-потерянном-ответе-c03).
