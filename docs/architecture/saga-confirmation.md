# Сага подтверждения передачи (UML sequence)

Оркестратор саги — сервис `transfer`. Источник: схема 12 плана миграции и `TransferService` Java-прототипа.

```mermaid
sequenceDiagram
    autonumber
    actor Op as Оператор источника
    participant Web as web (BFF)
    participant T as transfer
    participant DBt as БД transfer
    participant Inv as inventory
    participant C01 as C01 АИСТ-М
    participant K as Kafka
    participant LG as logistics-gateway
    participant C03 as C03 Логистика
    participant N as notification

    Op->>Web: Подтвердить (единицы, acceptPartial, idempotency key)
    Web->>T: POST /obligations/{id}/confirm [JWT]
    T->>DBt: find O07; status == PENDING?
    alt задание снято
        T-->>Web: TaskWithdrawn(status) → «не отправляйте»
    end
    T->>Inv: GET /internal/units/status?ids= (3 с, предохранитель, вне блокировки)
    Inv->>C01: точечный статус единиц
    C01-->>Inv: статусы
    Inv-->>T: present / withdrawn
    alt все выбыли
        T->>DBt: TX: CAS PENDING→REJECTED, outbox TransferRejected, аудит
        T-->>Web: Rejected(reason)
    else часть выбыла и acceptPartial = false
        T-->>Web: UnitsUnavailable(withdrawn)
    else на месте все или часть
        T->>DBt: TX: lock Остаток(клиника×вид) → перепроверка копии → NFR013 → CAS PENDING→CONFIRMED → INSERT Закрепления (UNIQUE unit_id WHERE CONFIRMED) → reserved += k → outbox: TransferConfirmed, [Shortfall], RequestTransport → аудит
        DBt-->>T: OK | DuplicateKey → UnitsTaken
        T-->>Web: Confirmed(units, shortfall) «закреплено, готовьте»
        Web-->>Op: 200 в том же запросе
        T->>K: relay: transfer.events, logistics.commands (ключ = O07)
        K->>LG: RequestTransport
        LG->>LG: inbox + строка REQUESTING (одна TX), аренда
        LG->>C03: есть заказ clientRef=o07-…? (сверка)
        C03-->>LG: нет
        LG->>C03: создать заказ
        C03-->>LG: shipmentRef
        LG->>K: logistics.replies: TransportAccepted (outbox, пока аренда наша)
        K->>T: TransportAccepted
        T->>DBt: inbox + CAS CONFIRMED→TRANSPORT_REQUESTED, transport_ref
        K->>N: TransferConfirmed → письмо врачу и оператору
        T-->>Web: SSE: статус TRANSPORT_REQUESTED
    end
```

## Компенсации и сбои

```mermaid
sequenceDiagram
    autonumber
    participant Inv as inventory
    participant K as Kafka
    participant T as transfer
    participant LG as logistics-gateway
    participant C03 as C03
    participant N as notification

    Inv->>K: UnitWithdrawn или UnitUpdated(QUARANTINE) (unit закреплена за O07)
    K->>T: UnitWithdrawn / UnitUpdated
    T->>T: TX: lock Остаток → unit_copy; если O07 CONFIRMED/TRANSPORT_REQUESTED → CAS → BROKEN, закрепления RELEASED, reserved −k, outbox: TransferBroken(reason), CancelTransport
    T->>K: logistics.commands: CancelTransport
    K->>LG: CancelTransport
    alt заказ уже создан
        LG->>C03: снять заказ
        LG->>K: TransportCancelled
    else отмена обогнала заказ
        LG->>LG: надгробие CANCELLED; поздний RequestTransport ничего не создаст
    end
    K->>N: TransferBroken → врач и оператор (FR049)
```

`RequestDeficitRecorded` пишет не `transfer`, а `matching` в ответ на `TransferBroken` (по `request_id` из события) и
предлагает следующий вариант. Замена равноценной единицей делается только **до** выбытия, через `ReplaceUnit`, и разрешена
в `CONFIRMED` и `TRANSPORT_REQUESTED` (ADR-0011). Карантин закреплённой единицы обрабатывается как выбытие (ADR-0027).

| Сбой | Поведение |
| --- | --- |
| Одна команда доставлена трижды | inbox + первичный ключ `transport_request(obligation_id)`: один заказ, один ответ |
| C03 создала заказ, ответ потерян | сверка по `clientRef` находит заказ, второго нет |
| Шлюз упал после записи REQUESTING, до вызова C03 | пока аренда жива, никто не трогает; после истечения сверщик создаёт один заказ |
| Шлюз упал после ответа C03, до записи исхода | сверщик находит заказ, второго нет |
| Восемь экземпляров шлюза одновременно | аренда строки: один заказ |
| C03 лежит дольше `max-attempts` | `TransportRejected(OUTCOME_UNKNOWN)`; O07 остаётся CONFIRMED; `EscalationRaised(TRANSPORT_OUTCOME_UNKNOWN)`; если сверка позже найдёт заказ — запоздалый `TransportAccepted` |
| АИСТ-М не ответила за 3 с | `SourceSystemUnavailable` для любой срочности: задание остаётся PENDING, «повторите»; для экстренного ещё `EscalationRaised(SOURCE_SYSTEM_UNAVAILABLE)` и аудит. По копии не закрепляем (ADR-0027) |
| Отмена O07 (`CancelObligation` или API) | `PENDING`/`CONFIRMED`/`TRANSPORT_REQUESTED` → `CANCELLED`, закрепления `RELEASED`, `TransferCancelled`; из `CONFIRMED` и `TRANSPORT_REQUESTED` ещё `CancelTransport` (ADR-0027) |
| Замена единицы и `TransportAccepted` одновременно меняют O07 | CAS пропускает одного; проигравшая замена не применяется, оператор повторяет её уже в `TRANSPORT_REQUESTED`, где замена разрешена |

## Долговечный таймер срока

Таблица `obligation` с частичным индексом `(deadline) WHERE status = 'PENDING'`; воркер каждые 5 с берёт просроченные,
в одной транзакции: `PENDING→EXPIRED`, новое O07 у следующего источника из `fallback_sources` и `TransferExpired(next_obligation_id)`;
без запасного источника — `TransferExpired` и `EscalationRaised(EXPIRED_NO_FALLBACK)`.
Экстренная заявка — окно в минутах (`UrgencyDict.confirm_window_minutes`).

## BPMN

Процесс «Подтверждение передачи» в BPMN 2.0: [`diagrams/bpmn/confirm-transfer.bpmn`](diagrams/bpmn/confirm-transfer.bpmn).
Один пул, дорожки «Оператор клиники-источника», «transfer», «inventory → C01 АИСТ-М», «logistics-gateway», «Перевозчик —
служба перевозки (C03, ADR-0022)». На схеме: проверка статуса O07 и ключа идемпотентности, точечный запрос в АИСТ-М до
транзакции, транзакция с блокировками, все результаты `ConfirmationResult`, заказ перевозки со сверкой и арендой,
таймерное граничное событие срока с эскалацией к следующему источнику, отмена из `PENDING` граничным событием,
событийные подпроцессы компенсаций (выбытие или карантин → `BROKEN` → `CancelTransport`, отмена после подтверждения,
отказ перевозчика) и отмены перевозки в шлюзе (надгробие). Отправка и доставка помечены как этап 7.
SVG-экспорт — отдельная задача ([diagrams/README.md](diagrams/README.md)).
