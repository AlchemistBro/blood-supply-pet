# Контракты сообщений: топики, события, команды

Все контракты лежат в `libs/contracts`: Go-типы (`libs/contracts/<контекст>/`), JSON Schema
(`libs/contracts/schemas/<контекст>/<Тип>.v1.json`) и примеры (`libs/contracts/examples/…`), регистрация типов —
`contracts.Registry` по файлам `registry_<контекст>.go`. Входящие тела HTTP от смежных систем (ИНТ-02) — не сообщения
Kafka: они регистрируются в `contracts.Inbound` и проверяются так же. Проверка совместимости в CI — `task contracts:check`.
Источник истины — схемы; таблицы ниже их пересказывают (`?` — необязательное поле). Тело сообщения — JSON. Заголовки Kafka:

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
| `inventory.events` | inventory | transfer, matching, planning, demand, notification, web(SSE) | `unit_id`; `clinic_id:kind` для снимков, норматива, порога и защищённого объёма; `sync_run_id`; `obligation_id` для квитанции ИНТ-05 |
| `inventory.commands` | transfer (пишет), владелец inventory | inventory | `obligation_id` |
| `demand.events` | demand | planning | `statistics_file_id` / `demand_estimate_id` / `clinic_share_version_id` |
| `planning.events` | planning | inventory (защищённый объём), notification, audit, web | `replenishment_plan_id` / `deficit_record_id` / `clinic_id:kind` |
| `matching.events` | matching | planning (дефицит), notification, web | `clinical_request_id` |
| `transfer.commands` | matching, planning (пишут), владелец transfer | transfer | `obligation_id` (для Open — `choice_id`) |
| `transfer.events` | transfer | matching, planning, notification, web, inventory (TRANSFERRED) | `obligation_id` |
| `logistics.commands` | transfer (пишет), владелец logistics-gateway | logistics-gateway | `obligation_id`; `request_id` для обновления оценок |
| `logistics.replies` | logistics-gateway | transfer | `obligation_id` |
| `logistics.events` | logistics-gateway | matching, planning | `source_clinic_id` |
| `security.events` | audit (детектор инцидентов, этап 7) | notification, дежурный / ИТ-служба | `incident_id` |
| `audit.events` | все сервисы | audit | `producer` (порядок внутри сервиса) |
| `*.dlt` | платформа | дежурный / ИТ-служба | — ядовитые сообщения после N попыток |

## События и команды по сервисам

### inventory

Исходный код единицы АИСТ-М (`aist_unit_code`) и номер донации хранятся только в базе `inventory` и ни в одно событие
не попадают: наружу идёт суррогат `unit_id` (ADR-0010). Статусы и причины выбытия — по ADR-0027: закрепляется только
`IN_STOCK`; истёкшая единица хранится как `DISCARDED`, причина `EXPIRED` есть только в `UnitWithdrawn`.
Правило публикации: выбытие из учёта (ISSUED, DISCARDED, TRANSFERRED, истечение срока) — только `UnitWithdrawn`;
`UnitUpdated` — только для единиц, которые остаются в учёте (IN_STOCK ↔ QUARANTINE, срок, место).

| Тип | Назначение | Поля |
| --- | --- | --- |
| `UnitRegistered` | Единица появилась в копии запаса по данным АИСТ-М | `unit_id`, `clinic_id`, `component_kind`, `blood_group`, `rh_positive`, `phenotype?`, `volume_ml`, `expires_at`, `storage_location`, `status`(IN_STOCK/QUARANTINE/ISSUED/DISCARDED/TRANSFERRED), `version` |
| `UnitUpdated` | У единицы в учёте изменился статус, место или срок | `unit_id`, `clinic_id`, `component_kind`, `status`(IN_STOCK/QUARANTINE), `expires_at?`, `storage_location?`, `version`, `changed_at` |
| `UnitWithdrawn` | Единица выбыла: выдана, списана, передана, истёк срок | `unit_id`, `clinic_id`, `component_kind`, `reason`(ISSUED/DISCARDED/TRANSFERRED/EXPIRED), `version` |
| `StockSnapshotPublished` | Снимок запаса клиники × вид × группа с водяным знаком (этап 3) | `clinic_id`, `component_kind`, `blood_group`, `unit_count`, `volume_ml`, `nearest_expiry?`, `watermark`, `synced_at` |
| `StockNormPublished` | Новый действующий норматив (FR012, FR013) | `clinic_id`, `component_kind`, `norm_ml`, `avg_monthly_ml`, `is_min_unit_applied?`, `version`, `calculated_at` |
| `StockBelowNorm` | Запас ниже норматива (FR014); только при переходе через порог сверху вниз | `clinic_id`, `component_kind`, `stock_volume_ml`, `norm_ml`, `detected_at` |
| `ProtectedVolumePublished` | Защищённый объём источника: норматив + фон, без подтверждённых обязательств (их учитывает `transfer`). Считает planning (`ProtectedVolumeCalculated`), публикует inventory как владелец запаса (ADR-0019) | `clinic_id`, `component_kind`, `volume_units` (в единицах, не в мл), `version`, `reason` |
| `SyncRunCompleted` | Сеанс обмена с АИСТ-М завершён (ИНТ-01, FR050); расхождения — только число | `sync_run_id`, `kind`(CHANGES/DAILY_SNAPSHOT/ON_DEMAND/UNIT_CHECK), `result`(SUCCESS/PARTIAL/FAILED), `clinic_id?`, `changes_count`, `discrepancies_count?`, `started_at?`, `finished_at` |
| `ConsumptionExportPublished` | Выгрузка расхода для demand (FR045) | `component_kind`, `clinic_id?`, `period_start`, `period_end`, `consumed_volume_ml` |
| `PatientAllocationAcknowledged` | АИСТ-М подтвердил приём закрепления (ИНТ-05, FR052) | `obligation_id`, `idempotency_key`, `acknowledged_at` |
| команда `AllocateUnitsToPatient` | От transfer после подтверждения по заявке врача (FR051, NFR017), этап 6 | `obligation_id`, `request_id` (AAD конверта), `idempotency_key`, `unit_ids[]`, `target_clinic_id`, `patient_case_sealed` (конверт номера случая K1 `v1.<kid>.<base64url>`, ADR-0020) |
| команда `ReleasePatientAllocation` | Снять закрепление при отмене/нарушении (FR052) | `obligation_id`, `idempotency_key`, `reason?`(OBLIGATION_CANCELLED/OBLIGATION_BROKEN) |

### demand

В событиях о файлах нет содержимого строк, только их число; построчные ошибки остаются в базе `demand`.

| Тип | Поля |
| --- | --- |
| `StatisticsFileAccepted` | `statistics_file_id`, `file_kind`(DISTRICT_PROFILE/CONSUMPTION_HISTORY/CLINIC_SHARE), `status`(ACCEPTED/PARTIAL), `accepted_rows?`, `rejected_rows`, `uploaded_by` |
| `StatisticsFileRejected` | `statistics_file_id`, `file_kind`, `reason`(DUPLICATE_FILE/INVALID_FORMAT/INVALID_ROWS), `rejected_rows`, `uploaded_by` |
| `DemandEstimateCalculated` | `demand_estimate_id`, `version_no`, `period_start`, `period_end` |
| `DemandEstimatePublished` | `demand_estimate_id`, `version_no`, `period_start`, `period_end`, `confirmed_by`, `clinic_share_version_id`, `lines[]{clinic_id, component_kind, blood_group?, demand_volume_ml}` |
| `ClinicSharesPublished` | `clinic_share_version_id`, `version_no`, `shares[]{district_id, clinic_id, share}` |

### planning

| Тип | Поля |
| --- | --- |
| `EmergencyForecastAccepted` | `emergency_forecast_id`, `scenario_version`, `territory` (название из `territory.name` ИНТ-02), `period_start`, `period_end`, `is_rough_estimate`, `lines_count` |
| `ReplenishmentPlanBuilt` | `replenishment_plan_id`, `demand_estimate_id`, `demand_estimate_version_no`, `emergency_forecast_id?`, `scenario_version?`, `stock_watermark`, `stock_synced_at?`, `delivery_estimated_at?`, `coverage_ratio`, `targets_count`, `proposals_count`, `deficits_count?`, `elapsed_ms` (версии входов — NFR009, время — NFR002) |
| `TransferProposed` | `proposal_id` (= `choice_id` в `OpenObligation`), `replenishment_plan_id`, `source_clinic_id`, `target_clinic_id`, `component_kind`, `blood_group`, `units`, `delivery_minutes`, `fallback_sources[]?` |
| `PlanDeficitRecorded` | `deficit_record_id`, `replenishment_plan_id`, `clinic_id`, `component_kind`, `blood_group?`, `uncovered_volume_ml`, `reason`(NO_SOURCE/SOURCE_REJECTED/NOT_CONFIRMED_IN_TIME/UNIT_WITHDRAWN/BELOW_UNIT_REMAINDER) |
| `ProtectedVolumeCalculated` | → inventory. `clinic_id`, `component_kind`, `volume_units` (в единицах: ceil((норматив + фон) / `typical_volume_ml` вида), без подтверждённых обязательств, ADR-0019), `replenishment_plan_id`, `version` (монотонная по клинике и виду), `calculated_at`, `reason` |
| команда → transfer `OpenObligation` | см. transfer |

Входящий прогноз ЧС (ИНТ-02, тело `POST /int/forecast`) — тип `int02.EmergencyForecast` в `contracts.Inbound`, схема
`libs/contracts/schemas/int02/EmergencyForecast.v1.json`: `scenario_version`, `scenario_kind?`, `scenario_name?`, `demo?`,
`territory{name, districts[]?{district_code, weight}}`, `period_start`, `period_end`,
`lines[]{clinic_code?, component_kind, blood_group?, volume_ml, share?}`. Белый список полей — `security.md` (NFR010).

### matching

Номера случая пациента в событиях `matching` нет ни в каком виде (NFR016); в ИНТ-05 он уходит только конвертом (ADR-0020).

| Тип | Поля |
| --- | --- |
| `ClinicalRequestCreated` | `clinical_request_id`, `clinic_id`, `component_kind`, `requested_units`, `urgency`, `blood_group`, `created_by` |
| `VariantsOffered` | `clinical_request_id`, `variants[]{variant_offer_id, rank_no, source_clinic_id, component_kind, blood_group, available_units, delivery_minutes, is_deficit_stock_used, matching_rule_id}`, `stock_watermark` |
| `DoctorChoiceRecorded` | `clinical_request_id`, `variant_offer_id`, `selected_by`, `is_local` |
| `RequestDeficitRecorded` | `clinical_request_id`, `clinic_id`, `component_kind`, `blood_group`, `uncovered_units`, `reason`(NO_ALLOWED_VARIANT/SOURCE_REJECTED/NOT_CONFIRMED_IN_TIME/UNIT_WITHDRAWN) |
| `ClinicalRequestCancelled` | `clinical_request_id`, `cancelled_by` |
| команда → transfer `OpenObligation` / `CancelObligation` | см. transfer |

### transfer

Во всех событиях `transfer.events` есть необязательные `obligation_version` (версия O07 после перехода: проекции
применяют событие, только если версия выше сохранённой), `source_clinic_id` и `target_clinic_id` (клиники обязательства:
web фильтрует живые статусы без запроса к `transfer`). В таблице они не повторяются; в `ObligationOpened` клиники
обязательны.

| Тип | Поля |
| --- | --- |
| команда `OpenObligation` | `choice_id` (идемпотентность), `request_id` (заявка или план), `origin`(DOCTOR_CHOICE/PLAN_PROPOSAL), `source_clinic_id`, `target_clinic_id`, `component_kind`, `requested_qty`, `urgency`, `confirm_window_seconds`, `fallback_sources[]?`, `spec{blood_group?, rh_positive?, phenotype?, min_expires_at?, rule_version}`, `patient_case_sealed?` (этап 6: конверт номера случая, ADR-0020). Открытого номера случая нет: обязательство ссылается на заявку через `request_id` |
| команда `CancelObligation` | `obligation_id`, `reason`(REQUEST_CANCELLED/PLAN_RECALCULATED/DUTY_OFFICER_DECISION) |
| команда `CancelRequestObligations` | `request_id`, `reason`(PLAN_RECALCULATED/REQUEST_CANCELLED), `only_pending`; надгробие запрещает позднее открытие заданий заявки, ADR-0030 |
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
| `TransferDispatched` | Этап 7: груз отправлен (TRANSPORT_REQUESTED → DISPATCHED). `obligation_id`, `request_id`, `at` |
| `TransferDelivered` | Этап 7: груз доставлен (DISPATCHED → DELIVERED). `obligation_id`, `request_id`, `at`, `conditions_violated?` (условия перевозки нарушены — к использованию не допускается, ПП РФ № 641, п. 98), `violations[]?`(TEMPERATURE/CONTAINER/DURATION/TEMPERATURE_NOT_RECORDED; п. 69 в, г, е; ADR-0022) |
| `EscalationRaised` | `obligation_id`, `request_id`, `reason`(EXPIRED_NO_FALLBACK/TRANSPORT_REFUSED/TRANSPORT_OUTCOME_UNKNOWN/SOURCE_SYSTEM_UNAVAILABLE/TEMPERATURE_EXCURSION), `at`. Пишется только тогда, когда нужен дежурный смены (ADR-0027); TEMPERATURE_EXCURSION — этап 7 |
| `ManualTransportAssigned` | Этап 7: дежурный назначил перевозку вручную (ADR-0023). `obligation_id`, `request_id`, `transport_ref`, `assigned_by`, `at` |
| `EscalationClosed` | Этап 7: дежурный закрыл эскалацию (ADR-0023). `obligation_id`, `request_id`, `reason?` (причина эскалации, как в `EscalationRaised`), `resolution`(MANUAL_TRANSPORT_ASSIGNED/RESOLVED/NO_ACTION_REQUIRED), `closed_by`, `at`. Свободного текста нет: комментарий остаётся в базе `transfer` и в аудите |

### logistics-gateway

| Тип | Поля |
| --- | --- |
| команда `RequestTransport` | `obligation_id`, `request_id`, `source_clinic_id`, `target_clinic_id`, `component_kind`, `quantity`, `temperature_regime`(COLD_2_6/FROZEN_MINUS_25/ROOM_20_24) (номеров единиц нет: замена единицы не трогает заказ; ПП РФ № 641, п. 68 в, ADR-0022) |
| команда `CancelTransport` | `obligation_id`, `reason`(UNIT_WITHDRAWN/UNIT_QUARANTINED/OBLIGATION_CANCELLED) |
| команда `RefreshDeliveryEstimates` | Этап 5: запросить у C03 свежие оценки доставки. `request_id`, `source_clinic_ids[]?` (пусто — по всем клиникам), `requested_at` |
| `TransportAccepted` | `obligation_id`, `shipment_ref` |
| `TransportRejected` | `obligation_id`, `reason`(REFUSED/OUTCOME_UNKNOWN), `details?` |
| `TransportCancelled` | `obligation_id` (и тогда, когда заказа не было) |
| `TransportDispatched` | Этап 7: груз принят уполномоченным работником службы перевозки (ПП РФ № 641, п. 73; ADR-0022). `obligation_id`, `shipment_ref?`, `dispatched_at`, `courier_ref` (служебный идентификатор без ФИО и пробелов) |
| `TransportDelivered` | `obligation_id`, `delivered_at`, `temperature_at_dispatch_c?`, `temperature_at_arrival_c?` (п. 69 е); этап 7 добавляет `departed_at?`, `duration_min?` (п. 69 в), `container_intact?` (п. 69 г), `temp_excursion?` (п. 69 е, приложение 3, п. 98), `courier_ref?` (п. 73). Все поля этапа 7 необязательные (ADR-0022, п. 6) |
| `DeliveryEstimatesPublished` | Этап 5, топик `logistics.events` (ADR-0014). `source_clinic_id`, `estimates[]{target_clinic_id, minutes}`, `estimated_at` |

Команды `RequestTransport`, `CancelTransport`, `RefreshDeliveryEstimates` идут в `logistics.commands`; ответы
`TransportAccepted`, `TransportRejected`, `TransportCancelled`, `TransportDispatched`, `TransportDelivered` — в
`logistics.replies`. Код `temperature_regime` — метка режима; предельные значения при перевозке — приложение 3
ПП РФ № 641 (эритроцитсодержащие не выше +10 °C, плазма и криопреципитат не выше −18 °C, тромбоциты +20…+24 °C).

### security

| Тип | Поля |
| --- | --- |
| `SecurityIncidentSuspected` | Этап 7: подозрение на инцидент с ПДн от детектора аномалий в журнале аудита (152-ФЗ ст. 21 ч. 3.1: уведомление за 24 ч, результаты за 72 ч; ADR-0025). `incident_id`, `rule` (код правила), `user_id` (учётная запись, не ФИО), `count`, `window_seconds`, `first_seen_at?`, `detected_at`. ПДн в событии нет |

### audit

| Тип | Поля |
| --- | --- |
| `AuditRecorded` | `occurred_at`, `user_id?`, `role?`, `clinic_id?`, `module`(inventory/demand/planning/matching/transfer/logistics/web/audit), `action`, `object_type`, `object_id`, `result`(SUCCESS/DENIED/ERROR), `details?` (без ПДн, без `patient_case_no`), `correlation_id?` |

Действие `READ_PATIENT_CASE` (NFR016) пишет только `matching`, в одной транзакции с выдачей номера случая
(`GET /requests/{id}/patient-case`, ADR-0020); отказ `matching` — та же запись с `result = DENIED`. `web` пишет
`LOGIN`, `LOGOUT`, `ACCESS_DENIED` (в том числе свой отказ в чтении номера до запроса к `matching`), ADR-0017.

## Платформа сообщений (`libs/messaging`)

- **Outbox**: таблица `outbox(id bigserial, message_id uuid unique, topic, key, type, schema_version, payload jsonb, headers jsonb, occurred_at, published_at)` в БД каждого сервиса. Запись только внутри транзакции бизнес-операции через `Journal`.
- **Relay**: воркер читает `WHERE published_at IS NULL ORDER BY id` пачками, шлёт идемпотентным продюсером (`acks=all`), помечает `published_at`; останавливается на первой ошибке, чтобы не нарушать порядок. Один активный relay на сервис через `pg_try_advisory_lock`.
- **Inbox**: таблица `inbox(message_id uuid pk, type, received_at)`; `FirstTime(tx, msg)` в транзакции обработчика; повтор упирается в первичный ключ.
- **Consumer**: ручной коммит после успешной транзакции; при ошибке N повторов с backoff, затем `*.dlt` и алерт.
- **Схемы**: `task contracts:check` валидирует примеры из `libs/contracts/examples/` по JSON Schema и прогоняет diff схем на обратную совместимость (запрет удаления обязательных полей).
