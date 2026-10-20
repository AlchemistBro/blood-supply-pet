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

    Inv->>K: UnitWithdrawn(unit закреплена за O07)
    K->>T: UnitWithdrawn
    T->>T: TX: unit_copy WITHDRAWN; если O07 CONFIRMED/TRANSPORT_REQUESTED и замены нет → CAS → BROKEN, закрепления RELEASED, reserved −k, outbox: TransferBroken, CancelTransport, RequestDeficitRecorded
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

| Сбой | Поведение |
| --- | --- |
| Одна команда доставлена трижды | inbox + первичный ключ `transport_request(obligation_id)`: один заказ, один ответ |
| C03 создала заказ, ответ потерян | сверка по `clientRef` находит заказ, второго нет |
| Шлюз упал после записи REQUESTING, до вызова C03 | пока аренда жива, никто не трогает; после истечения сверщик создаёт один заказ |
| Шлюз упал после ответа C03, до записи исхода | сверщик находит заказ, второго нет |
| Восемь экземпляров шлюза одновременно | аренда строки: один заказ |
| C03 лежит дольше `max-attempts` | `TransportRejected(OUTCOME_UNKNOWN)`; O07 остаётся CONFIRMED; эскалация дежурному; если сверка позже найдёт заказ — запоздалый `TransportAccepted` |
| АИСТ-М не ответила за 3 с | `SourceSystemUnavailable`: плановое задание остаётся PENDING, «повторите»; экстренное — решение дежурного (вслепую не закрепляем) |
| Замена единицы проиграла CAS ответу `TransportAccepted` | `NotAllowed(TRANSPORT_REQUESTED)`; оператор повторяет |

## Долговечный таймер срока

Таблица `obligation` с частичным индексом `(deadline) WHERE status = 'PENDING'`; воркер каждые 5 с берёт просроченные,
в одной транзакции: `PENDING→EXPIRED`, `EscalationRaised`, новое O07 у следующего источника из `fallback_sources`.
Экстренная заявка — окно в минутах (`UrgencyDict.confirm_window_minutes`).

## BPMN

Процесс «Подтверждение передачи оператором источника» моделируется в BPMN 2.0 (`docs/architecture/diagrams/bpmn/confirm-transfer.bpmn`,
экспорт SVG рядом): пулы «Оператор источника», «АС (transfer)», «C01 АИСТ-М», «C03 Логистика»; шлюзы «все на месте / часть / все выбыли / нет ответа»,
таймерное граничное событие на срок подтверждения, компенсационное событие «снять заказ». Файл создаётся на этапе 1, финализируется на этапе 9.
