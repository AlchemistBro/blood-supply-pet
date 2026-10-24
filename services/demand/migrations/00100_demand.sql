-- +goose Up
-- Схема сервиса demand (база demand, ФП2). Имена сущностей и полей - из словаря данных ПР6
-- (docs/architecture/data-model.md); ключи агрегатов - uuid v7 (ADR-0008), справочники ref_* - миграция 00002.
-- Участник (uploaded_by, confirmed_by) - идентификатор пользователя из токена: учётных записей в АС нет (ADR-0006).

CREATE TABLE statistics_file (
    statistics_file_id uuid         PRIMARY KEY,
    file_kind          varchar(24)  NOT NULL CHECK (file_kind IN ('DISTRICT_PROFILE', 'CONSUMPTION_HISTORY', 'CLINIC_SHARE')),
    file_name          varchar(256) NOT NULL,
    file_checksum      varchar(64)  NOT NULL UNIQUE CHECK (file_checksum ~ '^[0-9a-f]{64}$'),
    data_source        varchar(256),
    period_start       date,
    period_end         date,
    check_status       varchar(16)  NOT NULL CHECK (check_status IN ('ACCEPTED', 'PARTIAL', 'REJECTED')),
    rejected_rows      integer      NOT NULL DEFAULT 0 CHECK (rejected_rows >= 0),
    uploaded_by        varchar(128) NOT NULL,
    uploaded_at        timestamptz  NOT NULL,
    CHECK (period_end >= period_start),
    -- Источник и период обязательны у принятых файлов; у файла, отклонённого целиком (кодировка,
    -- заголовок), их может не быть, а запись нужна для истории загрузок и отказа в повторе.
    CHECK (check_status = 'REJECTED' OR (data_source IS NOT NULL AND period_start IS NOT NULL AND period_end IS NOT NULL))
);
COMMENT ON TABLE statistics_file IS 'Файл CSV территориального аналитика (FR016, FR017, FR045, FR046)';
COMMENT ON COLUMN statistics_file.file_checksum IS 'SHA-256 содержимого: повторная загрузка того же файла отклоняется';
COMMENT ON COLUMN statistics_file.check_status IS 'Результат автоматической проверки (FR017)';
CREATE INDEX statistics_file_uploaded_idx ON statistics_file (uploaded_at DESC);

CREATE TABLE file_row_error (
    file_row_error_id  bigint  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    statistics_file_id uuid    NOT NULL REFERENCES statistics_file (statistics_file_id),
    row_no             integer NOT NULL CHECK (row_no > 0),
    error_reason       text    NOT NULL,
    UNIQUE (statistics_file_id, row_no)
);
COMMENT ON TABLE file_row_error IS 'Отклонённая строка файла с причиной (FR017); ошибка всего файла - строка 1 (заголовок)';

CREATE TABLE district_profile (
    district_profile_id uuid    PRIMARY KEY,
    statistics_file_id  uuid    NOT NULL REFERENCES statistics_file (statistics_file_id),
    district_id         bigint  NOT NULL REFERENCES ref_district (district_id),
    population          integer NOT NULL CHECK (population > 0),
    UNIQUE (statistics_file_id, district_id)
);
COMMENT ON TABLE district_profile IS 'O04 Территориальный профиль района из файла аналитика (FR016, FR018)';

CREATE TABLE district_group_share (
    district_profile_id uuid         NOT NULL REFERENCES district_profile (district_profile_id),
    blood_group_code    varchar(4)   NOT NULL REFERENCES ref_blood_group (blood_group_code),
    group_share         numeric(5,4) NOT NULL CHECK (group_share BETWEEN 0 AND 1),
    PRIMARY KEY (district_profile_id, blood_group_code)
);
COMMENT ON TABLE district_group_share IS 'Доля группы AB0/Rh в районе s(r, g); сумма по району 1 +- 0,0001 проверяется при приёме файла (FR017)';

CREATE TABLE consumption_history (
    consumption_history_id uuid        PRIMARY KEY,
    statistics_file_id     uuid        REFERENCES statistics_file (statistics_file_id),
    history_source         varchar(8)  NOT NULL CHECK (history_source IN ('FILE', 'C01')),
    component_kind_code    varchar(16) NOT NULL REFERENCES ref_component_kind (component_kind_code),
    period_start           date        NOT NULL,
    period_end             date        NOT NULL,
    consumed_volume_ml     bigint      NOT NULL CHECK (consumed_volume_ml >= 0),
    CHECK (period_end >= period_start),
    CHECK ((history_source = 'FILE') = (statistics_file_id IS NOT NULL)),
    UNIQUE (component_kind_code, period_start, period_end, history_source)
);
COMMENT ON TABLE consumption_history IS 'O17 История фактического расхода по видам и периодам - база B(k, p) (FR045)';
COMMENT ON COLUMN consumption_history.consumed_volume_ml IS 'Израсходовано за период, мл (целые мл, как все объёмы)';

CREATE TABLE clinic_share_version (
    clinic_share_version_id uuid         PRIMARY KEY,
    version_no              bigint       NOT NULL UNIQUE CHECK (version_no > 0),
    statistics_file_id      uuid         NOT NULL UNIQUE REFERENCES statistics_file (statistics_file_id),
    basis                   varchar(256) NOT NULL,
    valid_from              date         NOT NULL
);
COMMENT ON TABLE clinic_share_version IS 'O16 Версия долей распределения район -> клиника с основанием (FR046)';

CREATE TABLE clinic_share (
    clinic_share_version_id uuid         NOT NULL REFERENCES clinic_share_version (clinic_share_version_id),
    district_id             bigint       NOT NULL REFERENCES ref_district (district_id),
    clinic_id               bigint       NOT NULL REFERENCES ref_clinic (clinic_id),
    clinic_share            numeric(5,4) NOT NULL CHECK (clinic_share > 0 AND clinic_share <= 1),
    PRIMARY KEY (clinic_share_version_id, district_id, clinic_id)
);
COMMENT ON TABLE clinic_share IS 'Доля потребности района w(r, c); сумма по району 1 +- 0,0001 проверяется при приёме файла';

CREATE TABLE estimate_status_dict (
    estimate_status_code varchar(16) PRIMARY KEY,
    estimate_status_name varchar(64) NOT NULL UNIQUE
);
INSERT INTO estimate_status_dict (estimate_status_code, estimate_status_name) VALUES
    ('CALCULATED', 'Рассчитана'),
    ('PUBLISHED', 'Опубликована'),
    ('REJECTED', 'Отклонена');

CREATE TABLE demand_estimate (
    demand_estimate_id      uuid         PRIMARY KEY,
    version_no              bigint       NOT NULL UNIQUE CHECK (version_no > 0),
    period_start            date         NOT NULL,
    period_end              date         NOT NULL,
    profile_file_id         uuid         NOT NULL REFERENCES statistics_file (statistics_file_id),
    clinic_share_version_id uuid         NOT NULL REFERENCES clinic_share_version (clinic_share_version_id),
    estimate_status_code    varchar(16)  NOT NULL REFERENCES estimate_status_dict (estimate_status_code),
    calculated_at           timestamptz  NOT NULL,
    confirmed_by            varchar(128),
    confirmed_at            timestamptz,
    CHECK (period_end >= period_start),
    -- FR020: публикация только с подтвердившим аналитиком.
    CHECK (estimate_status_code <> 'PUBLISHED' OR (confirmed_by IS NOT NULL AND confirmed_at IS NOT NULL))
);
COMMENT ON TABLE demand_estimate IS 'O05 Версия фоновой оценки потребности; публикуется только после подтверждения аналитиком (FR019, FR020)';

CREATE TABLE estimate_history_link (
    demand_estimate_id     uuid NOT NULL REFERENCES demand_estimate (demand_estimate_id),
    consumption_history_id uuid NOT NULL REFERENCES consumption_history (consumption_history_id),
    PRIMARY KEY (demand_estimate_id, consumption_history_id)
);
COMMENT ON TABLE estimate_history_link IS 'Какие периоды истории расхода вошли в версию оценки (NFR009)';

CREATE TABLE clinic_demand (
    clinic_demand_id    bigint        GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    demand_estimate_id  uuid          NOT NULL REFERENCES demand_estimate (demand_estimate_id),
    clinic_id           bigint        NOT NULL REFERENCES ref_clinic (clinic_id),
    component_kind_code varchar(16)   NOT NULL REFERENCES ref_component_kind (component_kind_code),
    blood_group_code    varchar(4)    REFERENCES ref_blood_group (blood_group_code),
    demand_volume_ml    numeric(14,2) NOT NULL CHECK (demand_volume_ml >= 0),
    UNIQUE NULLS NOT DISTINCT (demand_estimate_id, clinic_id, component_kind_code, blood_group_code)
);
COMMENT ON TABLE clinic_demand IS 'Строка версии оценки: фоновая потребность клиники по виду и группе за период, мл';

-- +goose Down
DROP TABLE clinic_demand;
DROP TABLE estimate_history_link;
DROP TABLE demand_estimate;
DROP TABLE estimate_status_dict;
DROP TABLE clinic_share;
DROP TABLE clinic_share_version;
DROP TABLE consumption_history;
DROP TABLE district_group_share;
DROP TABLE district_profile;
DROP TABLE file_row_error;
DROP TABLE statistics_file;
