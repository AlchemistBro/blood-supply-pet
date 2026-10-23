# Этап 1. Сага подтверждения через сеть

Цель: `transfer`, минимальный `inventory`, `logistics-gateway` и имитаторы C01/C03 воспроизводят всё, что умеет Java-прототип,
но уже как три сервиса с тремя базами. Требования: FR028, FR043 (приём задания), FR048, FR049; NFR004, NFR013, NFR017 (каркас).
Спецификация: `docs/domain/obligation-states.md`, `docs/architecture/saga-confirmation.md`, Java-тесты
`ConfirmationRaceFitnessTests`, `TransferScenarioTests`, `TransportSagaTests`, `TransportGatewayTests`.

Задание для агентов по пакетам работ, порядок и правила окружения: [01-saga-agents.md](01-saga-agents.md).

## Чеклист

### Контракты
- [ ] `libs/contracts/transfer`: команды `OpenObligation`, `CancelObligation`; события по `events.md`. JSON Schema + примеры.
- [ ] `libs/contracts/logistics`: `RequestTransport`, `CancelTransport`, `TransportAccepted`, `TransportRejected`, `TransportCancelled`.
- [ ] `libs/contracts/inventory`: `UnitRegistered`, `UnitUpdated`, `UnitWithdrawn`, `ProtectedVolumePublished` (минимум для этапа).
- [ ] OpenAPI `transfer`: `POST /obligations` (открыть, идемпотентно по `choice_id`), `POST /obligations/{id}/confirm`, `POST /obligations/{id}/replace`, `POST /obligations/{id}/cancel`, `GET /obligations/{id}`, `GET /obligations?requestId=|sourceClinicId=&status=`.
- [ ] OpenAPI `inventory`: `GET /internal/units/status?ids=`, dev-эндпоинты имитации (профиль `dev`).

### sim-aist (C01)
- [ ] В памяти: единицы по клиникам, статусы, `POST /dev/units`, `POST /dev/units/{id}/withdraw`, `GET /units/changes?since=`, `GET /units/status?ids=`, `GET /snapshot.csv`, `POST /allocations` (ИНТ-05, идемпотентный ключ), `POST /dev/faults` (`unavailableFor`, `slowMs`).
- [ ] Генерация стартового набора из `libs/testdata`.

### inventory (минимум этапа)
- [ ] Миграции: `unit`, `unit_status_history`, `sync_run`, `ref_*`, outbox/inbox, `audit_local`.
- [ ] Опрос изменений C01 каждые N секунд (конфиг), запись единиц с проверкой `source_version`, публикация `UnitRegistered`/`UnitUpdated`/`UnitWithdrawn`.
- [ ] `GET /internal/units/status`: статус из C01 с таймаутом; при недоступности — ответ из копии с `source: copy` и `watermark`.
- [ ] Публикация `ProtectedVolumePublished` из dev-эндпоинта (до появления `planning`).

### transfer
- [ ] Миграции: `source_stock`, `unit_copy`, `obligation`, `allocation` (частичный уникальный индекс), outbox/inbox, `audit_local`.
- [ ] Домен: `Obligation` с CAS-переходами, `ConfirmationResult`, `ReplacementResult`, спецификация замены, порядок блокировок.
- [ ] Приложение: `Open`, `Confirm`, `Replace`, `Cancel`, `ExpireOverdue` (таймер 5 с), `OnUnitEvent` (проекция + компенсация BROKEN), `OnLogisticsReply`.
- [ ] Адаптер `inventoryclient` с таймаутом 3 с и предохранителем; метрика отказов.
- [ ] Consumer: `transfer.commands`, `inventory.events`, `logistics.replies`; relay outbox.
- [ ] Фитнес-тесты (integration): четыре гонки; 50 параллельных подтверждений одной единицы; идемпотентность по ключу; `TaskWithdrawn`; `SourceSystemUnavailable`; эскалация с новым O07; правила замены (`NotAllowed`, `SpecMismatch`, `UnitUnavailable`, `UnitTaken`); команда уходит с ключом O07; дубль ответа отбрасывается inbox; нарушение шлёт `CancelTransport`; поздний `TransportAccepted` не оживляет BROKEN; `OUTCOME_UNKNOWN` оставляет CONFIRMED.

### logistics-gateway и sim-logistics (C03)
- [ ] `sim-logistics`: заказы без дедупликации (нарочно), `GET /shipments?clientRef=`, `POST /shipments`, `DELETE /shipments/{ref}`, `GET /estimates`, `POST /dev/faults` (`failNextCalls`, `loseNextSubmitResponse`, `refuseNext`).
- [ ] Миграции `transport_request`, `delivery_estimate`, outbox/inbox.
- [ ] `TransportGateway.drive()`: inbox + REQUESTING в одной tx → аренда → сверка по `clientRef` → создать → исход и ответ в outbox, пока аренда наша; `Reconciler` для просроченных аренд и `UNKNOWN`; `max-attempts` → `OUTCOME_UNKNOWN`; надгробие при отмене, обогнавшей заказ.
- [ ] Тесты (integration): одна команда трижды → один заказ; потерянный ответ → сверка; падение после REQUESTING; падение после ответа C03; восемь экземпляров; C03 лежит дольше лимита (две ветки); отмена после заказа / отмена обогнала / отказ перевозчика.

### Сквозное
- [ ] `tests/e2e/saga_test.go` на compose: четыре сценария `scripts/smoke.sh` Java-прототипа (подтверждение → заказ; выдача → BROKEN → отмена заказа; потерянный ответ; `docker compose kill logistics-gateway` → заказ уходит после старта).
- [ ] BPMN `confirm-transfer.bpmn` (черновик) + SVG; sequence-диаграммы в `saga-confirmation.md` сверены с кодом.
- [ ] Образы трёх сервисов и двух имитаторов публикуются в ghcr.io с тегом `edge`.

## Критерий выхода
`task test:fitness` зелёный; `task test:e2e` проходит четыре сценария, включая `kill -9` шлюза посреди заказа без второго заказа у C03.

## Проверка «тест действительно проверяет»
Убрать сверку → падают ≥ 3 теста шлюза. Убрать аренду → падают тесты двух экземпляров и падения. Убрать перепроверку
копии под блокировкой → падает гонка 2. Убрать частичный уникальный индекс → падают гонка 4 и тест 50 подтверждений.

## Открытые вопросы
- Экстренная заявка при недоступности АИСТ-М: подтверждать по копии с алертом (как в схеме 12) или всегда отказывать? Допущение этапа: отказывать (`SourceSystemUnavailable`), эскалация дежурному; флаг `BS_ALLOW_COPY_CONFIRM_EMERGENCY` по умолчанию `false`.
