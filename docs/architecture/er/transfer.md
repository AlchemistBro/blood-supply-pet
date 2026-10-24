# ER: transfer

Сгенерировано `task docs:er` из Up-миграций. Не редактировать вручную.

SHA-256 исходников: `d28d11fe6a106a87aecbbf6e6cf2995a3867fd20ee661cd13de6e6663efc80b8`.

Показаны столбцы, первичные и внешние ключи. CHECK, индексы, триггеры и данные не отображаются.
Справочники и платформенные таблицы включены так же, как при запуске сервиса.
Типы с параметрами показаны без параметров; массивы сохранены.

```mermaid
erDiagram
    allocation {
        uuid allocation_id PK
        uuid unit_id
        uuid obligation_id FK
        varchar status
        timestamptz created_at
        timestamptz closed_at
    }
    obligation ||--o{ allocation : "obligation_id"
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
    cancelled_request {
        uuid request_id PK
        text reason
        timestamptz cancelled_at
    }
    escalation {
        uuid escalation_id PK
        uuid obligation_id FK
        text reason
        timestamptz raised_at
        timestamptz closed_at
        text closed_by
        text resolution
        text comment
    }
    obligation ||--o{ escalation : "obligation_id"
    inbox {
        uuid message_id PK
        text message_type
        timestamptz received_at
    }
    obligation {
        uuid obligation_id PK
        uuid choice_id
        uuid request_id
        varchar origin
        bigint source_clinic_id FK
        bigint target_clinic_id FK
        varchar component_kind_code FK
        varchar blood_group_code
        integer requested_qty
        integer confirmed_qty
        varchar status
        varchar urgency_code FK
        timestamptz deadline
        bigint confirm_window_seconds
        bigint[] fallback_sources
        varchar spec_blood_group_code
        boolean spec_rh_positive
        text spec_phenotype
        timestamptz spec_min_expires_at
        varchar spec_rule_version
        text confirm_key
        text transport_ref
        text status_reason
        timestamptz escalated_at
        bigint version
        timestamptz created_at
        timestamptz updated_at
        jsonb protected_floors
        text patient_case_sealed
        uuid patient_allocation_key
        text patient_allocation_status
        timestamptz patient_allocation_at
        boolean clinical_use_blocked
        text[] transport_violations
    }
    ref_clinic ||--o{ obligation : "source_clinic_id"
    ref_clinic ||--o{ obligation : "target_clinic_id"
    ref_component_kind ||--o{ obligation : "component_kind_code"
    ref_urgency ||--o{ obligation : "urgency_code"
    obligation_transport {
        uuid obligation_id PK, FK
        jsonb payload
    }
    obligation ||--o| obligation_transport : "obligation_id"
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
    source_stock {
        bigint clinic_id PK, FK
        varchar component_kind_code PK, FK
        integer available_units
        integer reserved_units
        integer protected_volume
        bigint protected_version
        text protected_reason
    }
    ref_clinic ||--o{ source_stock : "clinic_id"
    ref_component_kind ||--o{ source_stock : "component_kind_code"
    unit_copy {
        uuid unit_id PK
        bigint clinic_id FK
        varchar component_kind_code FK
        varchar blood_group_code
        boolean rh_positive
        text phenotype
        bigint volume_ml
        timestamptz expires_at
        varchar unit_status_code FK
        text status_reason
        bigint version
        timestamptz updated_at
        uuid local_dispatch_obligation_id FK
        boolean clinical_use_blocked
    }
    ref_clinic ||--o{ unit_copy : "clinic_id"
    ref_component_kind ||--o{ unit_copy : "component_kind_code"
    ref_unit_status ||--o{ unit_copy : "unit_status_code"
    obligation |o--o{ unit_copy : "local_dispatch_obligation_id"
```

Исходники:

- [libs/messaging/migrations/00001_messaging.sql](../../../libs/messaging/migrations/00001_messaging.sql)
- [libs/refdata/migrations/00002_refdata.sql](../../../libs/refdata/migrations/00002_refdata.sql)
- [libs/refdata/migrations/00003_ref_unit_status.sql](../../../libs/refdata/migrations/00003_ref_unit_status.sql)
- [services/transfer/migrations/00100_transfer.sql](../../../services/transfer/migrations/00100_transfer.sql)
- [services/transfer/migrations/00110_protected_floors.sql](../../../services/transfer/migrations/00110_protected_floors.sql)
- [services/transfer/migrations/00120_cancelled_request.sql](../../../services/transfer/migrations/00120_cancelled_request.sql)
- [services/transfer/migrations/00130_patient_case.sql](../../../services/transfer/migrations/00130_patient_case.sql)
- [services/transfer/migrations/00150_transport_and_escalation.sql](../../../services/transfer/migrations/00150_transport_and_escalation.sql)
