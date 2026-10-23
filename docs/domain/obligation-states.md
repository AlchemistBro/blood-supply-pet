# Состояния обязательства O07 и сага подтверждения

Источник: схема 13 «Состояния обязательства O07», схема 12 «Сага подтверждения передачи», Java-прототип `TransferService`,
отступления этапа 1 — [ADR-0027](../adr/0027-stage1-deviations.md). Это спецификация агрегата `transfer.obligation`.
Менять без ADR нельзя.

## Состояния

```mermaid
stateDiagram-v2
    [*] --> PENDING: OpenObligation (выбор врача или предложение плана)
    PENDING --> CONFIRMED: оператор подтвердил все или часть единиц\n[проверка АИСТ-М, затем TX: NFR013 + NFR004]
    PENDING --> REJECTED: все единицы выбыли / отказ источника
    PENDING --> EXPIRED: таймер срока: эскалация,\nновое O07 PENDING у следующего источника (одна TX)
    PENDING --> CANCELLED: CancelObligation (заявка отменена / план пересчитан)
    CONFIRMED --> TRANSPORT_REQUESTED: TransportAccepted от logistics-gateway
    CONFIRMED --> CANCELLED: CancelObligation
    CONFIRMED --> BROKEN: закреплённая единица выбыла\nили ушла на карантин в АИСТ-М
    CONFIRMED --> CONFIRMED: замена равноценной единицы (ADR-0011)
    TRANSPORT_REQUESTED --> CANCELLED: CancelObligation → CancelTransport
    TRANSPORT_REQUESTED --> BROKEN: единица выбыла или на карантине\n→ CancelTransport
    TRANSPORT_REQUESTED --> TRANSPORT_REQUESTED: замена равноценной единицы
    TRANSPORT_REQUESTED --> DISPATCHED: перепроверка перед отправкой OK
    DISPATCHED --> DELIVERED: получатель подтвердил приём
    REJECTED --> [*]
    EXPIRED --> [*]
    CANCELLED --> [*]
    BROKEN --> [*]
    DELIVERED --> [*]
```

| Статус | Смысл | Держит закрепления | Конечный |
| --- | --- | --- | --- |
| `PENDING` | Ожидает подтверждения оператором источника; срок = `confirm_window` по срочности (минуты для экстренной) | нет | нет |
| `CONFIRMED` | Подтверждено, единицы закреплены (закрепления O18 в статусе `CONFIRMED`), команда `RequestTransport` в outbox | да | нет |
| `TRANSPORT_REQUESTED` | C03 принял заказ, `transport_ref` записан | да | нет |
| `DISPATCHED` | Отправлено курьером; закрепления → CONSUMED | — | нет |
| `DELIVERED` | Доставлено, принято клиникой | — | да |
| `REJECTED` | Отклонено: все единицы выбыли или источник отказал; подбор предложит следующий вариант | нет | да |
| `EXPIRED` | Не подтверждено в срок; новое O07 у следующего источника (`TransferExpired.next_obligation_id`); без запасного источника — `EscalationRaised(EXPIRED_NO_FALLBACK)` дежурному | нет | да |
| `CANCELLED` | Отменено командой `CancelObligation` или через API (заявка отменена, план пересчитан, решение дежурного); закрепления сняты; из `TRANSPORT_REQUESTED` ушёл `CancelTransport` | нет | да |
| `BROKEN` | Нарушено: закреплённая единица выбыла или ушла на карантин в АИСТ-М после подтверждения (`TransferBroken.reason` — причина выбытия из `UnitWithdrawn` или `UNIT_QUARANTINED`); закрепления сняты, `CancelTransport`; дефицит заявки записывает `matching` | нет | да |

Статус «ожидает подтверждения АИСТ-М» из ПР6 (FR051, FR052) реализуется **не** отдельным состоянием O07, а полем
`aist_ack_at` и статусом сообщения в `inventory.aist_outbox_message`: O07 остаётся `CONFIRMED`, UI показывает
«закрепление передаётся в АИСТ-М» до квитанции. Причина: состояние принадлежит другому контексту (ADR-0003).

Частичное подтверждение — не состояние: `confirmed_qty < requested_qty`, на недостачу событие `TransferShortfallRecorded`.

Закрепление O18 (`allocation`) имеет статусы `CONFIRMED` (держит единицу), `RELEASED` (снято: замена, отмена, нарушение)
и `CONSUMED` (единица отправлена). Строки не удаляются: по ним восстанавливается, куда уходила единица (ПП РФ № 641, п. 17–18, 54).

Единица закрепляется только в статусе `IN_STOCK`. Статусы `QUARANTINE`, `ISSUED`, `DISCARDED`, `TRANSFERRED` для подтверждения
и замены означают «единицы нет» (ПП РФ № 641, п. 62 а; ADR-0027).

## Правила переходов

1. Каждый переход — `UPDATE obligation SET status = :new, version = version + 1 WHERE id = :id AND status = :expected` (CAS).
   Из одновременных событий (оператор, таймер, отмена, ответ шлюза) выигрывает одно.
2. Порядок блокировок всегда одинаковый: строка `source_stock` (клиника × вид), затем строка `obligation`.
   Обработчик ответов логистики блокирует только `obligation`.
3. Проверка статуса единиц в АИСТ-М (через `inventory`) идёт **синхронно до транзакции**, вне блокировки, таймаут 3 с.
   Хранимого состояния «проверяется» нет: оператор получает ответ в том же HTTP-запросе.
4. Повтор подтверждения с тем же `idempotency_key` при статусе `CONFIRMED` возвращает тот же результат `Confirmed`.
5. После `DISPATCHED` отмена уже не компенсация: возврат единицы решает ОПК, автоматически свободной она не становится.

## Подтверждение: порядок шагов (`ConfirmTransfer`)

```
1. find O07; нет → InvalidRequest. status ≠ PENDING → CONFIRMED с тем же ключом ? Confirmed : TaskWithdrawn(status)
2. validate: непустой список разных единиц, не больше requested_qty, все единицы источника и того же вида
3. statuses = inventory.CheckUnits(ids)          // единственный синхронный вызов; SourceSystemUnavailable → ответ «повторите»,
                                                 // для экстренной срочности ещё TX: outbox EscalationRaised(SOURCE_SYSTEM_UNAVAILABLE)
4. present (IN_STOCK) / withdrawn (всё остальное, включая QUARANTINE) разделить
   все выбыли              → TX: CAS PENDING→REJECTED, событие TransferRejected, аудит → Rejected
   часть выбыла, !partial  → UnitsUnavailable(withdrawn), состояние не менялось
5. TX:
   lock source_stock(source_clinic, kind)
   перепроверить копию: не IN_STOCK (выбыла или карантин) → убрать; если !partial или пусто → UnitsUnavailable
   k = len(present); if stock.free - k < stock.protected_volume → ProtectedVolumeExceeded(maxConfirmable)
   CAS PENDING→CONFIRMED (confirmed_qty = k, confirm_key) ; fail → TaskWithdrawn
   INSERT allocation(unit, O07, CONFIRMED) × k   // UNIQUE (unit_id) WHERE status='CONFIRMED' → UnitsTaken
   source_stock.reserved += k
   outbox: TransferConfirmed, [TransferShortfallRecorded], command RequestTransport(key = O07 id)
   audit CONFIRMED
6. ответ Confirmed(units, shortfall) в том же запросе
```

Результаты (`ConfirmationResult`): `Confirmed`, `TaskWithdrawn`, `UnitsUnavailable`, `Rejected`, `UnitsTaken`,
`ProtectedVolumeExceeded`, `InvalidRequest`, `SourceSystemUnavailable`.

При недоступности АИСТ-М подтверждения по копии нет ни для какой срочности: задание остаётся `PENDING`, для экстренной
пишется `EscalationRaised(SOURCE_SYSTEM_UNAVAILABLE)`. Флаг `BS_ALLOW_COPY_CONFIRM_EMERGENCY` по умолчанию `false` (ADR-0027).

## Замена единицы (`ReplaceUnit`, ADR-0011)

Разрешена в `CONFIRMED` и `TRANSPORT_REQUESTED`. Кандидат проверяется по спецификации замены (источник, вид, группа,
резус, фенотип, годность на момент прибытия), по копии и точечным запросом в АИСТ-М. В одной транзакции: lock Остатка,
`casTouch` O07, старое закрепление → `RELEASED`, новое `CONFIRMED` (уникальный индекс → `UnitTaken`), резерв не меняется,
событие `TransferUnitReplaced`. Команда перевозчику не меняется: номеров единиц в `RequestTransport` нет.
Результаты: `Replaced`, `NotAllowed(status)`, `SpecMismatch`, `UnitUnavailable`, `UnitTaken`, `SourceSystemUnavailable`.

## Таймер срока (`ExpireOverdue`)

Долговечный таймер в БД `transfer` (индекс по `deadline WHERE status = 'PENDING'`), опрос каждые 5 с. Для каждого
просроченного в одной транзакции: CAS `PENDING→EXPIRED`, аудит; если есть `fallback_sources` — новое O07 `PENDING`
у следующего источника с `choice_id = uuid v5(пространство transfer, "escalation:" + old id)` и хвостом источников,
событие `TransferExpired(next_obligation_id)`; если запасного источника нет — `TransferExpired` без ссылки и
`EscalationRaised(EXPIRED_NO_FALLBACK)`. Пространство имён uuid v5 фиксировано навсегда (ADR-0027): повтор таймера
упирается в `UNIQUE (choice_id)`. Позднее подтверждение первого источника проигрывает CAS: две отправки невозможны.

## Компенсации

| Событие | Реакция `transfer` |
| --- | --- |
| `UnitWithdrawn` из `inventory` для закреплённой единицы | Сразу, в одной транзакции: CAS → `BROKEN`, все закрепления O07 → `RELEASED`, резерв уменьшается, в outbox `TransferBroken(reason = причина выбытия)`, `CancelTransport` (шлётся всегда: шлюз отвечает и тогда, когда заказа не было), аудит. Решение владельца от 23.10.2026, как в Java-прототипе (`InventoryProjection`). Замену равноценной единицей оператор делает только **до** выбытия, через `ReplaceUnit` |
| `UnitUpdated(status = QUARANTINE)` для закреплённой единицы | Как выбытие: CAS → `BROKEN`, `TransferBroken(reason = UNIT_QUARANTINED)`, `CancelTransport`. Возврат из карантина `BROKEN` не отменяет (ADR-0027) |
| `TransportRejected(REFUSED)` | O07 остаётся `CONFIRMED`, события `TransportRequestFailed` и `EscalationRaised(TRANSPORT_REFUSED)`: перевозку организует дежурный вручную |
| `TransportRejected(OUTCOME_UNKNOWN)` | O07 остаётся `CONFIRMED`, `TransportRequestFailed` и `EscalationRaised(TRANSPORT_OUTCOME_UNKNOWN)`; если сверка шлюза позже найдёт заказ, придёт запоздалый `TransportAccepted` |
| `TransportAccepted` для не-`CONFIRMED` O07 | Игнорируется с записью в аудит: `CancelTransport` уже ушла в момент нарушения или отмены |
| `CancelObligation` от `matching`/`planning` или отмена через API | `PENDING`/`CONFIRMED`/`TRANSPORT_REQUESTED` → `CANCELLED` (CAS), закрепления `RELEASED`, резерв снят, `TransferCancelled`; из `TRANSPORT_REQUESTED` ещё `CancelTransport`. Повтор идемпотентен; из конечных статусов и `DISPATCHED` отмены нет (ADR-0027) |

## Четыре гонки (обязательные фитнес-тесты)

1. **Защищённый объём**: параллельные подтверждения из одного Остатка вместе опускают его ниже защищённого объёма → проходят только те, что укладываются. Тест как в прототипе (ADR-0027): 60 единиц, защищённый объём 20, 50 параллельных подтверждений по одной единице → ровно 40 `Confirmed` и 10 `ProtectedVolumeExceeded`.
2. **Выдача во время подтверждения**: `UnitWithdrawn` приходит между проверкой АИСТ-М и транзакцией → перепроверка под блокировкой ловит, `UnitsUnavailable` или частичное.
3. **Позднее подтверждение после эскалации**: таймер перевёл в `EXPIRED` и открыл новое O07; подтверждение старого → `TaskWithdrawn(EXPIRED)`; единицы не закреплены дважды.
4. **Замена против подтверждения**: одна и та же единица одновременно — кандидат замены в O07-A и подтверждение в O07-B → уникальный индекс пускает одного, второй `UnitTaken`.

Плюс: 50 параллельных подтверждений одной единицы разными обязательствами → ровно одно `Confirmed` (NFR004).
