-- +goose Up
-- Общие справочники (схема ref ПР6) в базе каждого сервиса, данные сидируются из libs/refdata (ADR-0013).

CREATE TABLE ref_district (
    district_id   bigint       PRIMARY KEY,
    district_code varchar(32)  NOT NULL UNIQUE,
    district_name varchar(128) NOT NULL,
    population    bigint       NOT NULL CHECK (population > 0)
);
COMMENT ON TABLE ref_district IS 'Район города — территориальная единица оценки потребности (O04)';

CREATE TABLE ref_clinic (
    clinic_id       bigint       PRIMARY KEY,
    clinic_code     varchar(32)  NOT NULL UNIQUE,
    clinic_name     varchar(256) NOT NULL,
    district_id     bigint       NOT NULL REFERENCES ref_district (district_id),
    is_active       boolean      NOT NULL DEFAULT true,
    is_blood_center boolean      NOT NULL DEFAULT false
);
COMMENT ON TABLE ref_clinic IS 'Клиническая организация: источник, получатель, место хранения единиц';
COMMENT ON COLUMN ref_clinic.clinic_code IS 'Код организации в АИСТ-М (C01)';

CREATE TABLE ref_component_kind (
    component_kind_code varchar(16)  PRIMARY KEY,
    component_kind_name varchar(128) NOT NULL UNIQUE,
    is_group_applicable boolean      NOT NULL,
    typical_volume_ml   bigint       NOT NULL CHECK (typical_volume_ml > 0),
    shelf_life_days     integer      NOT NULL CHECK (shelf_life_days > 0),
    is_active           boolean      NOT NULL DEFAULT true
);
COMMENT ON COLUMN ref_component_kind.is_group_applicable IS 'Применимость группы AB0/Rh к расчёту потребности (FR019)';

CREATE TABLE ref_blood_group (
    blood_group_code varchar(4)   PRIMARY KEY,
    abo_group        varchar(2)   NOT NULL CHECK (abo_group IN ('O', 'A', 'B', 'AB')),
    rh_positive      boolean      NOT NULL,
    population_share numeric(5,4) NOT NULL CHECK (population_share >= 0 AND population_share <= 1),
    UNIQUE (abo_group, rh_positive)
);

CREATE TABLE ref_urgency (
    urgency_code           varchar(16) PRIMARY KEY,
    urgency_name           varchar(64) NOT NULL UNIQUE,
    confirm_window_minutes integer     NOT NULL CHECK (confirm_window_minutes > 0)
);
COMMENT ON COLUMN ref_urgency.confirm_window_minutes IS 'Срок подтверждения оператором источника (FR049)';

CREATE TABLE refdata_version (
    version    integer     PRIMARY KEY,
    applied_at timestamptz NOT NULL
);

-- +goose Down
DROP TABLE refdata_version;
DROP TABLE ref_urgency;
DROP TABLE ref_blood_group;
DROP TABLE ref_component_kind;
DROP TABLE ref_clinic;
DROP TABLE ref_district;
