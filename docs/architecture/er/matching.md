# ER: matching

Сгенерировано `task docs:er` из Up-миграций. Не редактировать вручную.

SHA-256 исходников: `2198f455f3afda10d6258bff0ccc870f359032997cec71551a00c7d84cf89610`.

Показаны столбцы, первичные и внешние ключи. CHECK, индексы, триггеры и данные не отображаются.
Справочники и платформенные таблицы включены так же, как при запуске сервиса.
Типы с параметрами показаны без параметров; массивы сохранены.

```mermaid
erDiagram
    audit_local {
        bigserial id PK
        timestamptz occurred_at
        text user_id
        text role
        bigint clinic_id
        text action
        text object_type
        text object_id
        text result
        jsonb details
        text correlation_id
    }
    clinical_request {
        uuid clinical_request_id PK
        bigint clinic_id FK
        text created_by
        text request_status_code FK
        jsonb payload
        jsonb match_result
        timestamptz created_at
        timestamptz updated_at
        bigint version
        jsonb match_summary
    }
    ref_clinic ||--o{ clinical_request : "clinic_id"
    request_status_dict ||--o{ clinical_request : "request_status_code"
    clinical_request_patient {
        uuid clinical_request_id PK, FK
        text patient_case_sealed
    }
    clinical_request ||--o| clinical_request_patient : "clinical_request_id"
    inbox {
        uuid message_id PK
        text message_type
        timestamptz received_at
    }
    matching_followup {
        uuid obligation_id PK
        bigint obligation_version PK
        text event_type PK
    }
    matching_input_projection {
        text input_key PK
        bigint source_version
        timestamptz occurred_at
        jsonb payload
    }
    matching_outgoing_choice {
        uuid choice_id PK
        uuid clinical_request_id FK
        bigint source_clinic_id
        integer requested_qty
    }
    clinical_request ||--o{ matching_outgoing_choice : "clinical_request_id"
    matching_rule {
        uuid matching_rule_id PK
        text rule_code
        bigint version_no
        timestamptz valid_from
        timestamptz valid_to
        jsonb payload
    }
    outbox {
        bigserial id PK
        uuid message_id
        text topic
        text key
        text message_type
        integer schema_version
        jsonb payload
        text correlation_id
        text causation_id
        text producer
        timestamptz occurred_at
        timestamptz published_at
    }
    ref_blood_group {
        varchar blood_group_code PK
        varchar abo_group
        boolean rh_positive
        numeric population_share
    }
    ref_clinic {
        bigint clinic_id PK
        varchar clinic_code
        varchar clinic_name
        bigint district_id FK
        boolean is_active
        boolean is_blood_center
    }
    ref_district ||--o{ ref_clinic : "district_id"
    ref_component_kind {
        varchar component_kind_code PK
        varchar component_kind_name
        boolean is_group_applicable
        bigint typical_volume_ml
        integer shelf_life_days
        boolean is_active
    }
    ref_district {
        bigint district_id PK
        varchar district_code
        varchar district_name
        bigint population
    }
    ref_unit_status {
        varchar unit_status_code PK
        varchar unit_status_name
        boolean is_available
    }
    ref_urgency {
        varchar urgency_code PK
        varchar urgency_name
        integer confirm_window_minutes
    }
    refdata_version {
        integer version PK
        timestamptz applied_at
    }
    request_constraint {
        uuid clinical_request_id PK, FK
        text constraint_code PK
        text constraint_value
    }
    clinical_request ||--o{ request_constraint : "clinical_request_id"
    request_status_dict {
        text status_code PK
    }
    variant_offer {
        uuid variant_offer_id PK
        uuid clinical_request_id FK
        integer rank_no
        jsonb payload
    }
    clinical_request ||--o{ variant_offer : "clinical_request_id"
    variant_offer_unit_link {
        uuid variant_offer_id PK, FK
        uuid unit_id PK
    }
    variant_offer ||--o{ variant_offer_unit_link : "variant_offer_id"
    variant_selection {
        uuid clinical_request_id PK, FK
        uuid variant_offer_id FK
        uuid choice_id
        text selected_by
        timestamptz selected_at
    }
    clinical_request ||--o| variant_selection : "clinical_request_id"
    variant_offer ||--o{ variant_selection : "variant_offer_id"
```

Исходники:

- [libs/messaging/migrations/00001_messaging.sql](../../../libs/messaging/migrations/00001_messaging.sql)
- [libs/refdata/migrations/00002_refdata.sql](../../../libs/refdata/migrations/00002_refdata.sql)
- [libs/refdata/migrations/00003_ref_unit_status.sql](../../../libs/refdata/migrations/00003_ref_unit_status.sql)
- [services/matching/migrations/00100_init.sql](../../../services/matching/migrations/00100_init.sql)
- [services/matching/migrations/00110_matching.sql](../../../services/matching/migrations/00110_matching.sql)
- [services/matching/migrations/00120_request_projection_index.sql](../../../services/matching/migrations/00120_request_projection_index.sql)
- [services/matching/migrations/00130_followup.sql](../../../services/matching/migrations/00130_followup.sql)
- [services/matching/migrations/00140_outgoing_choice.sql](../../../services/matching/migrations/00140_outgoing_choice.sql)
- [services/matching/migrations/00150_cancellation_result.sql](../../../services/matching/migrations/00150_cancellation_result.sql)
- [services/matching/migrations/00160_request_summary.sql](../../../services/matching/migrations/00160_request_summary.sql)
