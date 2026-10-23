# Контракты сообщений: топики, события, команды

Все контракты лежат в `libs/contracts`: Go-типы (`libs/contracts/<topic>/`), JSON Schema (`libs/contracts/schemas/*.json`),
проверка совместимости в CI (`task contracts:check`). Тело сообщения — JSON. Заголовки Kafka:

| Заголовок | Значение |
| --- | --- |
| `message-id` | uuid v7; inbox отбрасывает дубли по нему |
| `message-type` | имя типа, например `TransferConfirmed` |
| `schema-version` | целое, растёт при обратно совместимых изменениях |
| `correlation-id` | сквозной идентификатор запроса пользователя или входного события |
| `causation-id` | `message-id` сообщения, вызвавшего это |
| `producer` | имя сервиса |
| `occurred-at` | RFC 3339 |

Ключ сообщения — идентификатор агрегата (строка uuid), поэтому все сообщения по одному агрегату идут в одну
партицию и читаются по порядку. Доставка «хотя бы один раз»; идемпотентность обеспечивает inbox получателя.

## Топики

| Топик | Владелец (пишет) | Читают | Ключ |
| --- | --- | --- | --- |
| `inventory.events` | inventory | transfer, matching, planning, demand, web(SSE) | `unit_id` или `clinic_id:kind` для снимков |
| `inventory.commands` | transfer (пишет), владелец inventory | inventory | `obligation_id` |
| `demand.events` | demand | planning | `demand_estimate_id` |
| `planning.events` | planning | matching (нет), notification, audit, web | `replenishment_plan_id` / `deficit_record_id` |
| `matching.events` | matching | planning (дефицит), notification, web | `clinical_request_id` |
| `transfer.commands` | matching, planning (пишут), владелец transfer | transfer | `obligation_id` (для Open — `choice_id`) |
| `transfer.events` | transfer | matching, planning, notification, web, inventory (TRANSFERRED) | `obligation_id` |
| `logistics.commands` | transfer (пишет), владелец logistics-gateway | logistics-gateway | `obligation_id` |
| `logistics.replies` | logistics-gateway | transfer | `obligation_id` |
| `logistics.events` | logistics-gateway | matching, planning | `source_clinic_id` |
| `audit.events` | все сервисы | audit | `producer` (порядок внутри сервиса) |
| `*.dlt` | платформа | дежурный / ИТ-служба | — ядовитые сообщения после N попыток |

## События и команды по сервисам

### inventory

Исходный код единицы АИСТ-М (`aist_unit_code`) и номер донации хранятся только в базе `inventory` и ни в одно событие
не попадают: наружу идёт суррогат `unit_id` (ADR-0010). Статусы и причины выбытия — по ADR-0027: закрепляется только
`IN_STOCK`; истёкшая единица хранится как `DISCARDED`, причина `EXPIRED` есть только в `UnitWithdrawn`.

| Тип | Назначение | Поля |
| --- | --- | --- |
| `UnitRegistered` | Единица появилась в копии запаса по данным АИСТ-М | `unit_id`, `clinic_id`, `component_kind`, `blood_group`, `rh_positive`, `phenotype?`, `volume_ml`, `expires_at`, `storage_location`, `status`, `version` |
| `UnitUpdated` | Изменился статус, место или срок | `unit_id`, `clinic_id`, `component_kind`, `status`(IN_STOCK/QUARANTINE/ISSUED/DISCARDED/TRANSFERRED), `expires_at?`, `storage_location?`, `version`, `changed_at` |
| `UnitWithdrawn` | Единица выбыла: выдана, списана, передана, истёк срок | `unit_id`, `clinic_id`, `component_kind`, `reason`(ISSUED/DISCARDED/TRANSFERRED/EXPIRED), `version` |
| `StockSnapshotPublished` | Снимок запаса клиники × вид × группа с водяным знаком | `clinic_id`, `component_kind`, `blood_group`, `unit_count`, `volume_ml`, `nearest_expiry`, `watermark`, `synced_at` |
| `StockNormPublished` | Новый действующий норматив | `clinic_id`, `component_kind`, `norm_ml`, `avg_monthly_ml`, `version`, `calculated_at` |
| `ProtectedVolumePublished` | Защищённый объём источника (норматив + фон + обязательства, считает planning, публикует inventory как владелец запаса) | `clinic_id`, `component_kind`, `volume_units` (в единицах, не в мл: Остаток `transfer` считает единицы), `version`, `reason` |
| `SyncRunCompleted` | Сеанс обмена с АИСТ-М завершён | `sync_run_id`, `kind`, `result`, `changes_count`, `finished_at` |
| `ConsumptionExportPublished` | Выгрузка расхода для demand (FR045) | `component_kind`, `period_start`, `period_end`, `consumed_volume_ml` |
| `PatientAllocationAcknowledged` | АИСТ-М подтвердил приём закрепления (ИНТ-05) | `obligation_id`, `idempotency_key`, `acknowledged_at` |
| команда `AllocateUnitsToPatient` | От transfer после подтверждения по заявке врача (FR051), этап 6 | `obligation_id`, `idempotency_key`, `unit_ids`, `request_id`, `patient_case_sealed` (конверт номера случая K1, ADR-0020), `target_clinic_id` |
| команда `ReleasePatientAllocation` | Снять закрепление при отмене/нарушении | `obligation_id`, `idempotency_key` |

### demand

| Тип | Поля |
| --- | --- |
| `StatisticsFileAccepted` / `StatisticsFileRejected` | `statistics_file_id`, `file_kind`, `rejected_rows`, `uploaded_by` |
| `DemandEstimateCalculated` | `demand_estimate_id`, `version_no`, `period_start`, `period_end` |
| `DemandEstimatePublished` | `demand_estimate_id`, `version_no`, `confirmed_by`, `lines[]{clinic_id, component_kind, blood_group?, demand_volume_ml}`, `clinic_share_version_id` |
| `ClinicSharesPublished` | `clinic_share_version_id`, `version_no`, `shares[]{district_id, clinic_id, share}` |

### planning

| Тип | Поля |
| --- | --- |
| `EmergencyForecastAccepted` | `emergency_forecast_id`, `scenario_version`, `territory`, `period_start`, `period_end`, `is_rough_estimate`, `lines_count` |
| `ReplenishmentPlanBuilt` | `replenishment_plan_id`, `demand_estimate_id`, `emergency_forecast_id?`, `stock_watermark`, `coverage_ratio`, `targets_count`, `proposals_count`, `elapsed_ms` |
| `TransferProposed` | `proposal_id`, `replenishment_plan_id`, `source_clinic_id`, `target_clinic_id`, `component_kind`, `blood_group`, `units`, `delivery_minutes`, `fallback_sources[]` |
| `PlanDeficitRecorded` | `deficit_record_id`, `replenishment_plan_id`, `clinic_id`, `component_kind`, `blood_group?`, `uncovered_volume_ml`, `reason` |
| команда → transfer `OpenObligation` | см. transfer |

### matching

| Тип | Поля |
| --- | --- |
| `ClinicalRequestCreated` | `clinical_request_id`, `clinic_id`, `component_kind`, `requested_units`, `urgency`, `blood_group`, `created_by` (без `patient_case_no`) |
| `VariantsOffered` | `clinical_request_id`, `variants[]{variant_offer_id, rank_no, source_clinic_id, component_kind, blood_group, available_units, delivery_minutes, is_deficit_stock_used, matching_rule_id}`, `stock_watermark` |
| `DoctorChoiceRecorded` | `clinical_request_id`, `variant_offer_id`, `selected_by`, `is_local` |
| `RequestDeficitRecorded` | `clinical_request_id`, `clinic_id`, `component_kind`, `blood_group`, `uncovered_units`, `reason`(NO_ALLOWED_VARIANT/SOURCE_REJECTED/NOT_CONFIRMED_IN_TIME/UNIT_WITHDRAWN) |
| `ClinicalRequestCancelled` | `clinical_request_id`, `cancelled_by` |
| команда → transfer `OpenObligation` / `CancelObligation` | см. transfer |

### transfer

| Тип | Поля |
| --- | --- |
| команда `OpenObligation` | `choice_id` (идемпотентность), `request_id` (заявка или план), `origin`(DOCTOR_CHOICE/PLAN_PROPOSAL), `source_clinic_id`, `target_clinic_id`, `component_kind`, `requested_qty`, `urgency`, `confirm_window_seconds`, `fallback_sources[]`, `spec{blood_group?, rh_positive?, phenotype?, min_expires_at?, rule_version}`, `patient_case_sealed?` (этап 6: конверт номера случая, ADR-0020). Открытого номера случая нет: обязательство ссылается на заявку через `request_id` |
| команда `CancelObligation` | `obligation_id`, `reason`(REQUEST_CANCELLED/PLAN_RECALCULATED/DUTY_OFFICER_DECISION) |
| `ObligationOpened` | `obligation_id`, `request_id`, `origin`, `source_clinic_id`, `target_clinic_id`, `component_kind`, `requested_qty`, `deadline` |
| `TransferConfirmed` | `obligation_id`, `request_id`, `units[]`, `quantity`, `operator` |
| `TransferShortfallRecorded` | `obligation_id`, `request_id`, `component_kind`, `shortfall` |
| `TransferRejected` | `obligation_id`, `request_id`, `reason`(UNITS_WITHDRAWN/SOURCE_REFUSED) |
| `TransferExpired` | `obligation_id`, `request_id`, `next_obligation_id?` (новое задание у следующего источника, если он был) |
| `TransferCancelled` | `obligation_id`, `request_id`, `reason`(REQUEST_CANCELLED/PLAN_RECALCULATED/DUTY_OFFICER_DECISION) |
| `TransferBroken` | `obligation_id`, `request_id`, `unit_id`, `reason`(ISSUED/DISCARDED/TRANSFERRED/EXPIRED — причина выбытия из `UnitWithdrawn`; QUARANTINE — карантин закреплённой единицы, ADR-0027) |
| `TransferUnitReplaced` | `obligation_id`, `old_unit_id`, `new_unit_id`, `rule_version` |
| `TransportRequested` | `obligation_id`, `shipment_ref` |
| `TransportRequestFailed` | `obligation_id`, `reason`(REFUSED/OUTCOME_UNKNOWN), `details?` |
| `TransferDispatched` / `TransferDelivered` | `obligation_id`, `at` (этап 7) |
| `EscalationRaised` | `obligation_id`, `request_id`, `reason`(EXPIRED_NO_FALLBACK/TRANSPORT_REFUSED/TRANSPORT_OUTCOME_UNKNOWN/SOURCE_SYSTEM_UNAVAILABLE), `at`. Пишется только тогда, когда нужен дежурный смены (ADR-0027) |

### logistics-gateway

| Тип | Поля |
| --- | --- |
| команда `RequestTransport` | `obligation_id`, `request_id`, `source_clinic_id`, `target_clinic_id`, `component_kind`, `quantity`, `temperature_regime` (номеров единиц нет: замена единицы не трогает заказ; режим по виду компонента, значения демо, ПП РФ № 641 п. 68 в, ADR-0022) |
| команда `CancelTransport` | `obligation_id`, `reason`(UNIT_WITHDRAWN/UNIT_QUARANTINED/OBLIGATION_CANCELLED) |
| `TransportAccepted` | `obligation_id`, `shipment_ref` |
| `TransportRejected` | `obligation_id`, `reason`(REFUSED/OUTCOME_UNKNOWN), `details?` |
| `TransportCancelled` | `obligation_id` (и тогда, когда заказа не было) |
| `TransportDelivered` | `obligation_id`, `delivered_at`, `temperature_at_dispatch_c?`, `temperature_at_arrival_c?`. Топик `logistics.replies`. Заготовка этапа 7: контракт есть, обработки в этапе 1 нет (ADR-0022) |

Команды `RequestTransport`, `CancelTransport` идут в `logistics.commands`; ответы `TransportAccepted`, `TransportRejected`,
`TransportCancelled`, `TransportDelivered` — в `logistics.replies`. Коды `temperature_regime` в контракте этапа 1:
`COLD_2_6`, `FROZEN_MINUS_25`, `ROOM_20_24` (демо, ADR-0010).
| `DeliveryEstimatesPublished` | `source_clinic_id`, `estimates[]{target_clinic_id, minutes}`, `estimated_at` |

### audit

| Тип | Поля |
| --- | --- |
| `AuditRecorded` | `occurred_at`, `user_id?`, `role?`, `clinic_id?`, `module`(inventory/demand/planning/matching/transfer/web), `action`, `object_type`, `object_id`, `result`(SUCCESS/DENIED/ERROR), `details` (без ПДн, без `patient_case_no`), `correlation_id` |

Действие `READ_PATIENT_CASE` пишется `web` при каждом показе номера случая (NFR016).

## Платформа сообщений (`libs/messaging`)

- **Outbox**: таблица `outbox(id bigserial, message_id uuid unique, topic, key, type, schema_version, payload jsonb, headers jsonb, occurred_at, published_at)` в БД каждого сервиса. Запись только внутри транзакции бизнес-операции через `Journal`.
- **Relay**: воркер читает `WHERE published_at IS NULL ORDER BY id` пачками, шлёт идемпотентным продюсером (`acks=all`), помечает `published_at`; останавливается на первой ошибке, чтобы не нарушать порядок. Один активный relay на сервис через `pg_try_advisory_lock`.
- **Inbox**: таблица `inbox(message_id uuid pk, type, received_at)`; `FirstTime(tx, msg)` в транзакции обработчика; повтор упирается в первичный ключ.
- **Consumer**: ручной коммит после успешной транзакции; при ошибке N повторов с backoff, затем `*.dlt` и алерт.
- **Схемы**: `task contracts:check` валидирует примеры из `libs/contracts/examples/` по JSON Schema и прогоняет diff схем на обратную совместимость (запрет удаления обязательных полей).
