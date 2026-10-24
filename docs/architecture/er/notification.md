# ER: notification

Сгенерировано `task docs:er` из Up-миграций. Не редактировать вручную.

SHA-256 исходников: `a1ee9b47b2d019d1d419ba47693d6dfef43e171dbd4ddd939af6d37792a09ffc`.

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
    inbox {
        uuid message_id PK
        text message_type
        timestamptz received_at
    }
    notification {
        uuid notification_id PK
        text kind
        uuid obligation_id
        uuid request_id
        uuid source_message_id
        text recipient_role
        bigint recipient_clinic_id
        text recipient_email
        text subject
        bigint source_clinic_id
        bigint target_clinic_id
        text component_kind_code
        integer quantity
        text urgency_code
        timestamptz deadline
        text reason
        text status
        integer attempts
        timestamptz next_attempt_at
        text last_error
        timestamptz created_at
        timestamptz sent_at
    }
    obligation_projection {
        uuid obligation_id PK
        uuid request_id
        text origin
        bigint source_clinic_id
        bigint target_clinic_id
        text component_kind_code
        integer requested_qty
        text urgency_code
        timestamptz deadline
        timestamptz opened_at
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
```

Исходники:

- [libs/messaging/migrations/00001_messaging.sql](../../../libs/messaging/migrations/00001_messaging.sql)
- [libs/refdata/migrations/00002_refdata.sql](../../../libs/refdata/migrations/00002_refdata.sql)
- [libs/refdata/migrations/00003_ref_unit_status.sql](../../../libs/refdata/migrations/00003_ref_unit_status.sql)
- [services/notification/migrations/00100_notification.sql](../../../services/notification/migrations/00100_notification.sql)
- [services/notification/migrations/00110_notification_rules.sql](../../../services/notification/migrations/00110_notification_rules.sql)
