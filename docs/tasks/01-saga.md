# Этап 1. Сага подтверждения через сеть

Цель: `transfer`, минимальный `inventory`, `logistics-gateway` и имитаторы C01/C03 воспроизводят всё, что умеет Java-прототип,
но уже как три сервиса с тремя базами. Требования: FR028, FR043 (приём задания), FR048, FR049; NFR004, NFR013, NFR017 (каркас).
Спецификация: `docs/domain/obligation-states.md`, `docs/architecture/saga-confirmation.md`, Java-тесты
`ConfirmationRaceFitnessTests`, `TransferScenarioTests`, `TransportSagaTests`, `TransportGatewayTests`.
Отступления от канона и прототипа (карантин, отмена, `EscalationRaised`, причина `ISSUED`, uuid v5, отказ экстренному
заданию при недоступном АИСТ-М): [ADR-0027](../adr/0027-stage1-deviations.md).

## Чеклист

### Контракты
- [ ] `libs/contracts/transfer`: команды `OpenObligation`, `CancelObligation`; события по `events.md`, включая `TransferCancelled`, `TransferBroken(request_id, reason)`, `EscalationRaised`. JSON Schema + примеры.
- [ ] `libs/contracts/logistics`: `RequestTransport` (с `temperature_regime`), `CancelTransport`, `TransportAccepted`, `TransportRejected`, `TransportCancelled`; `TransportDelivered` — заготовка этапа 7.
- [ ] `libs/contracts/inventory`: `UnitRegistered`, `UnitUpdated`, `UnitWithdrawn` (причины `ISSUED`, `DISCARDED`, `TRANSFERRED`, `EXPIRED`), `ProtectedVolumePublished` (минимум для этапа).
- [ ] Тест контрактов: ни одна схема этапа не содержит номера случая пациента.
- [ ] OpenAPI `transfer`: `POST /obligations` (открыть, идемпотентно по `choice_id`), `POST /obligations/{id}/confirm`, `POST /obligations/{id}/replace`, `POST /obligations/{id}/cancel`, `GET /obligations/{id}`, `GET /obligations?requestId=|sourceClinicId=&status=`.
- [ ] OpenAPI `inventory`: `GET /internal/units/status?ids=`, dev-эндпоинты имитации (профиль `dev`).

### sim-aist (C01)
- [ ] В памяти: единицы по клиникам, статусы, `POST /dev/units`, `POST /dev/units/{id}/withdraw`, `GET /units/changes?since=`, `GET /units/status?ids=`, `GET /snapshot.csv`, `POST /allocations` (ИНТ-05, идемпотентный ключ), `POST /dev/faults` (`unavailableFor`, `slowMs`).
- [ ] Генерация стартового набора из `libs/testdata`.

### inventory (минимум этапа)
- [ ] Миграции: `unit`, `unit_status_history`, `sync_run`, `ref_*`, outbox/inbox, `audit_local`.
- [ ] Справочник `ref_unit_status` (`IN_STOCK`, `QUARANTINE`, `ISSUED`, `DISCARDED`, `TRANSFERRED`, признак `is_available`).
- [ ] Опрос изменений C01 каждые N секунд (конфиг), запись единиц с проверкой `source_version`, публикация `UnitRegistered`/`UnitUpdated`/`UnitWithdrawn`. Строки `unit` не удаляются.
- [ ] `GET /internal/units/status`: статус из C01 с таймаутом; при недоступности — ответ из копии с `source: copy` и `watermark`.
- [ ] Публикация `ProtectedVolumePublished` из dev-эндпоинта (до появления `planning`).

### transfer
- [ ] Миграции: `source_stock`, `unit_copy`, `obligation`, `allocation` (частичный уникальный индекс, индекс `(unit_id, created_at)`, строки не удаляются), outbox/inbox, `audit_local`. `clinic_id` — `bigint`.
- [ ] Домен: `Obligation` с CAS-переходами, `ConfirmationResult`, `ReplacementResult`, спецификация замены, порядок блокировок.
- [ ] Приложение: `Open`, `Confirm`, `Replace`, `Cancel`, `ExpireOverdue` (таймер 5 с), `OnUnitEvent` (проекция + компенсация BROKEN при выбытии и карантине), `OnLogisticsReply`.
- [ ] Адаптер `inventoryclient` с таймаутом 3 с и предохранителем; метрика отказов.
- [ ] Consumer: `transfer.commands`, `inventory.events`, `logistics.replies`; relay outbox.
- [ ] Сценарии (integration, по `TransferScenarioTests`): `repeatedChoiceDoesNotOpenSecondObligation`, `repeatedConfirmationWithSameKeyIsIdempotent`, `allUnitsWithdrawnRejectsObligation`, `aistMDownLeavesObligationPending`, `timerEscalatesToNextSourceAndExpiresWithoutOne`, `replacementFollowsSpecFromDoctorsChoice`, `replacementIsNotAllowedBeforeConfirmation`, `withdrawalOfAllocatedUnitBreaksObligationAndReleasesUnits`.
- [ ] Сценарии ADR-0027 (новые): `quarantinedUnitIsNeverAllocated` (подтверждение и замена единицей на карантине не проходят), карантин закреплённой единицы → `BROKEN(UNIT_QUARANTINED)` и `CancelTransport`, отмена из `PENDING`, `CONFIRMED` и `TRANSPORT_REQUESTED` (из последнего ещё `CancelTransport`), повтор отмены идемпотентен, отмена конечного O07 отклоняется, экстренное задание при недоступном АИСТ-М → `SourceSystemUnavailable` и `EscalationRaised(SOURCE_SYSTEM_UNAVAILABLE)`.
- [ ] Сага с логистикой (integration, по `TransportSagaTests`): `confirmationPublishesRequestTransportKeyedByObligation`, `acceptedReplyMovesObligationOnceEvenIfDeliveredTwice`, `withdrawalAfterTransportRequestedCompensatesWithCancelTransport`, `lateAcceptanceDoesNotReviveBrokenObligation`, `unknownOutcomeKeepsConfirmedAndLateAcceptanceStillLands`.
- [ ] Фитнес-тесты (integration, по `ConfirmationRaceFitnessTests`): гонка 1 — 50 подтверждений из одного Остатка, 40 `Confirmed` и 10 `ProtectedVolumeExceeded`; 50 параллельных подтверждений одной единицы → одно `Confirmed`, 49 `UnitsTaken`; гонка 2 — выдача во время подтверждения; гонка 3 — позднее подтверждение после эскалации; гонка 4 — замена против подтверждения; `partialConfirmationAndRemainderNeverShipOneUnitTwice`.

### logistics-gateway и sim-logistics (C03)
- [ ] `sim-logistics`: заказы без дедупликации (нарочно), `GET /shipments?clientRef=`, `POST /shipments`, `DELETE /shipments/{ref}`, `GET /estimates`, `POST /dev/faults` (`failNextCalls`, `loseNextSubmitResponse`, `refuseNext`).
- [ ] Миграции `transport_request`, `delivery_estimate`, outbox/inbox.
- [ ] `TransportGateway.drive()`: inbox + REQUESTING в одной tx → аренда → сверка по `clientRef` → создать → исход и ответ в outbox, пока аренда наша; `Reconciler` для просроченных аренд и `UNKNOWN`; `max-attempts` → `OUTCOME_UNKNOWN`; надгробие при отмене, обогнавшей заказ.
- [ ] Тесты (integration, все одиннадцать из `TransportGatewayTests`): `commandFromKafkaGetsOneShipmentAndReplyInKafka`, `duplicateDeliveriesOfACommandCreateOneShipment`, `lostResponseIsReconciledInsteadOfResubmitted`, `crashAfterRecordingButBeforeCallingC03`, `crashAfterC03AcceptedButBeforeRecordingTheOutcome`, `twoGatewayInstancesDrivingTheSameRequestCreateOneShipment`, `carrierDownTooLongGivesHonestUnknownAndNoBlindResubmit`, `unknownOutcomeThatActuallyReachedCarrierEndsAccepted`, `cancelAfterAcceptanceCancelsAtCarrier`, `cancelOvertakingTheRequestPreventsTheShipment`, `carrierRefusalIsReported`.

### Сквозное
- [ ] `tests/e2e/saga_test.go` на compose, пять сценариев: четыре из `scripts/smoke.sh` Java-прототипа (подтверждение → заказ; выдача → BROKEN → отмена заказа; потерянный ответ C03 → ровно один заказ; шлюз остановлен → подтверждение проходит, заказ уходит после старта) и пятый — `docker compose kill logistics-gateway` посреди заказа (после записи `REQUESTING`) → после `docker compose start` у C03 ровно один заказ.
- [ ] BPMN `confirm-transfer.bpmn` (черновик) + SVG; sequence-диаграммы в `saga-confirmation.md` сверены с кодом.
- [ ] Образы трёх сервисов и двух имитаторов публикуются в ghcr.io с тегом `edge`.

## Критерий выхода
`task test:fitness` зелёный; `task test:e2e` проходит пять сценариев, включая `kill -9` шлюза посреди заказа без второго заказа у C03.

## Проверка «тест действительно проверяет»
Убрать сверку → падают ≥ 3 теста шлюза. Убрать аренду → падают тесты двух экземпляров и падения. Убрать перепроверку
копии под блокировкой → падает гонка 2. Убрать частичный уникальный индекс → падают гонка 4 и тест 50 подтверждений.
Разрешить закрепление `QUARANTINE` → падает `quarantinedUnitIsNeverAllocated`.

## Решённые вопросы
- Экстренная заявка при недоступности АИСТ-М: всегда отказ `SourceSystemUnavailable` и `EscalationRaised(SOURCE_SYSTEM_UNAVAILABLE)`;
  флаг `BS_ALLOW_COPY_CONFIRM_EMERGENCY` по умолчанию `false` ([ADR-0027](../adr/0027-stage1-deviations.md), 23.10.2026).
- Выбытие и карантин закреплённой единицы: сразу `BROKEN`, замена только до выбытия (решение владельца от 23.10.2026, ADR-0027).
