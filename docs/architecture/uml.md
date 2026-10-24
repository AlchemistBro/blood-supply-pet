# UML: состояния O07 и заказа перевозки, сверка шлюза

Диаграммы в Mermaid. Спецификация переходов O07 — канон [obligation-states.md](../domain/obligation-states.md),
отступления этапа 1 — [ADR-0027](../adr/0027-stage1-deviations.md); при расхождении верен канон, а не эта страница.
Последовательности основного пути саги и компенсаций — [saga-confirmation.md](saga-confirmation.md), процесс целиком —
BPMN [confirm-transfer.bpmn](diagrams/bpmn/confirm-transfer.bpmn).

## Агрегаты передачи и заявки

```mermaid
classDiagram
    class Obligation {
        UUID ID
        UUID RequestID
        int64 SourceClinicID
        int64 TargetClinicID
        Status Status
        int64 Version
        bool ClinicalUseBlocked
    }
    class ReplacementSpec {
        string BloodGroup
        string Phenotype
        time MinExpiresAt
        string RuleVersion
    }
    class Allocation {
        UUID UnitID
        UUID ObligationID
        string Status
    }
    class Stock {
        int64 ClinicID
        string ComponentKind
        int Available
        int Reserved
        int Protected
    }
    class ClinicalRequest {
        UUID RequestID
        int64 ClinicID
        string ComponentKind
        int RequestedUnits
        string Urgency
    }
    class Variant {
        int Rank
        int64 SourceClinicID
        bool IsLocal
        string RuleVersion
    }
    class EncryptedPatientCase {
        UUID RequestID
        string KeyID
        bytes Ciphertext
    }
    Obligation *-- ReplacementSpec
    Obligation "1" -- "0..*" Allocation
    Stock ..> Obligation : собственный остаток источника
    ClinicalRequest "1" -- "0..*" Variant
    ClinicalRequest "1" -- "1" EncryptedPatientCase
    ClinicalRequest ..> Obligation : только идентификатор через Kafka
```

`Obligation`, закрепления и остаток хранятся в transfer; заявка, варианты и зашифрованный номер — в matching.
Связь между базами логическая, без внешнего ключа. Номер случая раскрывается отдельной операцией после аудита.

## Состояния обязательства O07

Переходы те же, что в каноне; закрепляющие состояния собраны в составное «Единицы закреплены», потому что выход из
обоих одинаков: выбытие или карантин закреплённой единицы и отмена.

```mermaid
stateDiagram-v2
    direction LR
    [*] --> PENDING: OpenObligation
    PENDING --> HELD: Confirm [АИСТ-М ответила, единицы IN_STOCK, защищённый объём не нарушен]
    PENDING --> REJECTED: Confirm [все единицы выбыли]
    PENDING --> EXPIRED: таймер срока
    PENDING --> CANCELLED: CancelObligation

    state "Единицы закреплены" as HELD {
        [*] --> CONFIRMED
        CONFIRMED --> CONFIRMED: ReplaceUnit
        CONFIRMED --> TRANSPORT_REQUESTED: TransportAccepted
        TRANSPORT_REQUESTED --> TRANSPORT_REQUESTED: ReplaceUnit
    }

    HELD --> BROKEN: UnitWithdrawn или UnitUpdated(QUARANTINE)
    HELD --> CANCELLED: CancelObligation
    HELD --> DISPATCHED: проверка перед отправкой [из TRANSPORT_REQUESTED]
    DISPATCHED --> DELIVERED: приёмка получателем

    REJECTED --> [*]
    EXPIRED --> [*]
    CANCELLED --> [*]
    BROKEN --> [*]
    DELIVERED --> [*]

    note right of EXPIRED
        Новое O07 PENDING у следующего источника
        (choice_id = uuid v5) или эскалация дежурному
    end note
    note right of BROKEN
        Закрепления RELEASED, CancelTransport,
        возврат из карантина BROKEN не отменяет
    end note
```

Повтор `Confirm` с тем же ключом в `CONFIRMED` или `TRANSPORT_REQUESTED` состояния не меняет и возвращает прежний
`Confirmed`. Отказ перевозчика (`TransportRejected`) тоже не меняет состояния: O07 остаётся `CONFIRMED`, дежурному уходит
эскалация. `DISPATCHED` и `DELIVERED` реализуются на этапе 7 (ADR-0022).

| Переход | Условие | Действия в той же транзакции |
| --- | --- | --- |
| `PENDING → CONFIRMED` | Точечный статус АИСТ-М получен до транзакции; под блокировкой Остатка копия перепроверена; `free − k ≥ protected_volume` | `allocation` × k (`UNIQUE (unit_id) WHERE status = 'CONFIRMED'`), `reserved += k`, outbox: `TransferConfirmed`, [`TransferShortfallRecorded`], `RequestTransport`; аудит |
| `PENDING → REJECTED` | Все единицы выбыли по ответу АИСТ-М | `TransferRejected(UNITS_WITHDRAWN)`; аудит |
| `PENDING → EXPIRED` | Срок вышел, опрос каждые 5 с | Новое O07 и `TransferExpired(next_obligation_id)` или `TransferExpired` и `EscalationRaised(EXPIRED_NO_FALLBACK)`; аудит |
| `PENDING → CANCELLED` | `CancelObligation` или отмена через API | `TransferCancelled`; аудит |
| `CONFIRMED → TRANSPORT_REQUESTED` | `TransportAccepted` из inbox | `transport_ref`, `TransportRequested` |
| `CONFIRMED`, `TRANSPORT_REQUESTED` → `BROKEN` | Закреплённая единица выбыла или ушла на карантин | Закрепления `RELEASED`, `reserved −= k`, `TransferBroken(reason)`, `CancelTransport(UNIT_WITHDRAWN \| UNIT_QUARANTINED)`; аудит |
| `CONFIRMED`, `TRANSPORT_REQUESTED` → `CANCELLED` | `CancelObligation` или отмена через API | Закрепления `RELEASED`, резерв снят, `TransferCancelled`, `CancelTransport(OBLIGATION_CANCELLED)`; аудит |
| `CONFIRMED`, `TRANSPORT_REQUESTED` → то же | `ReplaceUnit` по спецификации замены (ADR-0011) | `casTouch`, старое закрепление `RELEASED`, новое `CONFIRMED`, `TransferUnitReplaced`; резерв и заказ перевозки не меняются |

Каждый переход — `UPDATE … WHERE status = :expected` (CAS): из одновременных событий (оператор, таймер, отмена, ответ
шлюза) выигрывает одно.

## Состояния заказа перевозки (transport_request)

Строка `transport_request` в базе `logistics`, одна на обязательство (первичный ключ `obligation_id`).

```mermaid
stateDiagram-v2
    direction LR
    [*] --> REQUESTING: RequestTransport / inbox и строка одной TX
    [*] --> CANCELLED: CancelTransport раньше заказа / надгробие, TransportCancelled

    REQUESTING --> ACCEPTED: сверка нашла заказ или C03 приняла / TransportAccepted
    REQUESTING --> REJECTED: C03 отказала / TransportRejected(REFUSED)
    REQUESTING --> REQUESTING: C03 недоступна, попыток меньше 5 / пауза 1 с × 2^n
    REQUESTING --> UNKNOWN: 5 неудач подряд / TransportRejected(OUTCOME_UNKNOWN)
    REQUESTING --> CANCELLED: cancel_requested / снять заказ, если есть, TransportCancelled

    UNKNOWN --> UNKNOWN: C03 недоступна / перепроверка через 1 мин
    UNKNOWN --> ACCEPTED: сверка нашла заказ / запоздалый TransportAccepted
    UNKNOWN --> FAILED: сверка заказа не нашла / без ответа, повторно не отправляем
    UNKNOWN --> CANCELLED: cancel_requested / снять заказ, если есть, TransportCancelled

    ACCEPTED --> CANCELLED: cancel_requested / снять заказ у C03, TransportCancelled

    REJECTED --> [*]
    FAILED --> [*]
    CANCELLED --> [*]

    note right of REQUESTING
        С C03 говорит только владелец аренды:
        lease_owner, lease_until = now + 30 с.
        Исход пишется CAS по lease_owner
    end note
    note left of CANCELLED
        Надгробие: строка без маршрута,
        поздний RequestTransport упирается
        в первичный ключ, заказа нет
    end note
```

| Правило | Как держится |
| --- | --- |
| Один заказ на обязательство | Первичный ключ `obligation_id`; inbox отсекает повтор доставки; сверка по `clientRef = o07-<id>` всегда раньше отправки |
| Один исполнитель на строку | Аренда: `Claim` берёт строку, только если `lease_until` истёк; при `kill -9` аренда истекает сама, и строку подбирает сверщик |
| Исход опоздавшего не применяется | `Finish` и `FailAttempt` — `UPDATE … WHERE lease_owner = :owner` |
| Очередь сверщика | Частичный индекс по `next_attempt_at` для `REQUESTING`, `UNKNOWN` и `ACCEPTED` с `cancel_requested` |
| Отмена конечного заказа | `CancelTransport` при `REJECTED` или `FAILED` ставит флаг и сразу отвечает `TransportCancelled`: снимать у перевозчика нечего, статус не меняется |

## Сверка шлюза при потерянном ответе C03

Перевозчик принял заказ, но ответ до шлюза не дошёл. Повтор вслепую создал бы вторую машину: перевозчик заказы не
дедуплицирует. Сценарий из теста `lostResponseIsReconciledInsteadOfResubmitted`; так же завершается падение шлюза после
ответа C03 до записи исхода (`crashAfterC03AcceptedButBeforeRecordingTheOutcome`).

```mermaid
sequenceDiagram
    autonumber
    participant K as Kafka
    participant A as logistics-gateway (экземпляр A)
    participant DB as БД logistics
    participant C03 as C03 служба перевозки
    participant R as сверщик (любой экземпляр)
    participant T as transfer

    K->>A: RequestTransport(O07), ключ = obligation_id
    A->>DB: TX: inbox.FirstTime + INSERT REQUESTING (ON CONFLICT DO NOTHING)
    A->>DB: Claim: lease_owner = A, lease_until = now + 30 с
    DB-->>A: строка под арендой A
    A->>C03: GET /shipments?clientRef=o07-…
    C03-->>A: заказа нет
    A->>C03: POST /shipments (clientRef = o07-…)
    Note over C03: заказ S-1 создан
    C03--xA: ответ потерян (таймаут)
    A->>DB: FailAttempt WHERE lease_owner = A: attempts = 1, next_attempt_at = now + 1 с, аренда снята
    Note over A,DB: статус остаётся REQUESTING, вторую отправку не делаем

    alt шлюз упал после ответа C03, до записи исхода
        Note over A: kill -9: строка REQUESTING с арендой A
        Note over DB: через 30 с lease_until истекает
    end

    R->>DB: Due(now): REQUESTING с истёкшей арендой и наступившей попыткой
    R->>DB: Claim: lease_owner = R
    R->>C03: GET /shipments?clientRef=o07-…
    C03-->>R: активный заказ S-1
    R->>DB: TX: Finish(ACCEPTED, shipment_ref = S-1) WHERE lease_owner = R + outbox TransportAccepted + аудит
    Note over R,DB: сверка вместо повтора: у C03 по-прежнему один заказ
    DB-->>K: ретранслятор outbox: logistics.replies, TransportAccepted
    K->>T: TransportAccepted
    T->>T: TX: inbox + CAS CONFIRMED → TRANSPORT_REQUESTED, transport_ref = S-1
```

Если C03 не отвечает дольше пяти попыток, строка переходит в `UNKNOWN`, transfer получает
`TransportRejected(OUTCOME_UNKNOWN)` и эскалирует дежурному, а сверщик продолжает раз в минуту: найденный заказ даёт
запоздалый `TransportAccepted`, ненайденный — `FAILED` без повторной отправки (дежурный мог заказать машину сам).
