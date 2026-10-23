-- +goose Up
-- Схема сервиса transfer (база transfer): перенос V1__transfer_core.sql Java-прототипа с именами из ПР6
-- (docs/architecture/data-model.md). Платформенные таблицы (outbox, inbox, audit_local, ref_*) создаются миграциями 00001-00002.
-- Номера случая пациента здесь нет и быть не должно: заявку представляет только request_id.

-- Остаток источника (клиника × вид): точка сериализации подтверждений (NFR013). Строку блокируют подтверждение,
-- замена, отмена и обработчики событий inventory, всегда первой, до строки обязательства.
CREATE TABLE source_stock (
    clinic_id           bigint      NOT NULL REFERENCES ref_clinic (clinic_id),
    component_kind_code varchar(16) NOT NULL REFERENCES ref_component_kind (component_kind_code),
    available_units     integer     NOT NULL DEFAULT 0 CHECK (available_units >= 0),
    reserved_units      integer     NOT NULL DEFAULT 0 CHECK (reserved_units >= 0),
    protected_volume    integer     NOT NULL DEFAULT 0 CHECK (protected_volume >= 0),
    protected_version   bigint      NOT NULL DEFAULT 0,
    protected_reason    text,
    PRIMARY KEY (clinic_id, component_kind_code)
);
COMMENT ON COLUMN source_stock.available_units IS 'Единиц в наличии по копии (IN_STOCK; карантин не считается)';
COMMENT ON COLUMN source_stock.reserved_units IS 'Действующих закреплений (allocation CONFIRMED)';
COMMENT ON COLUMN source_stock.protected_volume IS 'Защищённый объём в единицах из ProtectedVolumePublished (NFR013), демо';

-- Копия единиц из inventory.events. Применяется только более новая версия единицы.
CREATE TABLE unit_copy (
    unit_id             uuid        PRIMARY KEY,
    clinic_id           bigint      NOT NULL REFERENCES ref_clinic (clinic_id),
    component_kind_code varchar(16) NOT NULL REFERENCES ref_component_kind (component_kind_code),
    blood_group_code    varchar(4)  NOT NULL,
    rh_positive         boolean     NOT NULL,
    phenotype           text,
    volume_ml           bigint      NOT NULL DEFAULT 0 CHECK (volume_ml >= 0),
    expires_at          timestamptz NOT NULL,
    unit_status_code    varchar(16) NOT NULL REFERENCES ref_unit_status (unit_status_code),
    status_reason       text,
    version             bigint      NOT NULL,
    updated_at          timestamptz NOT NULL
);
CREATE INDEX unit_copy_stock_idx ON unit_copy (clinic_id, component_kind_code, unit_status_code);
COMMENT ON COLUMN unit_copy.unit_status_code IS 'Статус по справочнику ref_unit_status; доступность — ref_unit_status.is_available';
COMMENT ON COLUMN unit_copy.status_reason IS 'Причина выбытия из UnitWithdrawn (EXPIRED хранится здесь при статусе DISCARDED)';
COMMENT ON COLUMN unit_copy.phenotype IS 'Только для проверки спецификации замены (ADR-0011); не логируется и не публикуется';

-- Обязательство на передачу O07. Каждый переход статуса — CAS по status и version.
CREATE TABLE obligation (
    obligation_id          uuid        PRIMARY KEY,
    choice_id              uuid        NOT NULL,
    request_id             uuid        NOT NULL,
    origin                 varchar(16) NOT NULL CHECK (origin IN ('DOCTOR_CHOICE', 'PLAN_PROPOSAL')),
    source_clinic_id       bigint      NOT NULL REFERENCES ref_clinic (clinic_id),
    target_clinic_id       bigint      NOT NULL REFERENCES ref_clinic (clinic_id),
    component_kind_code    varchar(16) NOT NULL REFERENCES ref_component_kind (component_kind_code),
    blood_group_code       varchar(4),
    requested_qty          integer     NOT NULL CHECK (requested_qty > 0),
    confirmed_qty          integer     NOT NULL DEFAULT 0 CHECK (confirmed_qty >= 0 AND confirmed_qty <= requested_qty),
    status                 varchar(24) NOT NULL CHECK (status IN ('PENDING', 'CONFIRMED', 'TRANSPORT_REQUESTED', 'DISPATCHED',
                                                                 'DELIVERED', 'REJECTED', 'EXPIRED', 'CANCELLED', 'BROKEN')),
    urgency_code           varchar(16) NOT NULL REFERENCES ref_urgency (urgency_code),
    deadline               timestamptz NOT NULL,
    confirm_window_seconds bigint      NOT NULL CHECK (confirm_window_seconds > 0),
    fallback_sources       bigint[]    NOT NULL DEFAULT '{}',
    spec_blood_group_code  varchar(4),
    spec_rh_positive       boolean,
    spec_phenotype         text,
    spec_min_expires_at    timestamptz,
    spec_rule_version      varchar(64) NOT NULL,
    confirm_key            text,
    transport_ref          text,
    status_reason          text,
    escalated_at           timestamptz,
    version                bigint      NOT NULL DEFAULT 0,
    created_at             timestamptz NOT NULL,
    updated_at             timestamptz NOT NULL,
    CONSTRAINT obligation_choice_uk UNIQUE (choice_id),
    CONSTRAINT obligation_source_not_target CHECK (source_clinic_id <> target_clinic_id)
);
-- Долговечный таймер срока подтверждения: опрос просроченных PENDING.
CREATE INDEX obligation_pending_deadline_idx ON obligation (deadline) WHERE status = 'PENDING';
CREATE INDEX obligation_request_idx ON obligation (request_id);
CREATE INDEX obligation_source_status_idx ON obligation (source_clinic_id, status);
COMMENT ON COLUMN obligation.fallback_sources IS 'Следующие источники для эскалации по таймеру, по порядку ранжирования';
COMMENT ON COLUMN obligation.escalated_at IS 'Когда экстренное задание передано дежурному из-за недоступной АИСТ-М: одна эскалация на задание';
COMMENT ON COLUMN obligation.spec_phenotype IS 'Спецификация замены (ADR-0011): квазиидентификатор, не логируется';

-- Закрепление единицы O18. NFR004 держит частичный уникальный индекс: одна единица — одно действующее закрепление.
-- Строки не удаляются никогда: история показывает, куда уходила единица (прослеживаемость, ПП РФ № 641).
CREATE TABLE allocation (
    allocation_id uuid        PRIMARY KEY,
    unit_id       uuid        NOT NULL,
    obligation_id uuid        NOT NULL REFERENCES obligation (obligation_id),
    status        varchar(16) NOT NULL CHECK (status IN ('CONFIRMED', 'RELEASED', 'CONSUMED')),
    created_at    timestamptz NOT NULL,
    closed_at     timestamptz,
    CHECK ((status = 'CONFIRMED') = (closed_at IS NULL))
);
CREATE UNIQUE INDEX allocation_one_confirmed_per_unit ON allocation (unit_id) WHERE status = 'CONFIRMED';
CREATE INDEX allocation_unit_history_idx ON allocation (unit_id, created_at);
CREATE INDEX allocation_obligation_idx ON allocation (obligation_id, status);

-- +goose StatementBegin
CREATE FUNCTION allocation_no_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'строки allocation не удаляются: это история закреплений единицы (прослеживаемость)';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER allocation_no_delete BEFORE DELETE ON allocation FOR EACH ROW EXECUTE FUNCTION allocation_no_delete();

-- +goose Down
DROP TABLE allocation;
DROP FUNCTION allocation_no_delete();
DROP TABLE obligation;
DROP TABLE unit_copy;
DROP TABLE source_stock;
