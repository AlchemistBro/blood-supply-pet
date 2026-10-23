-- +goose Up
-- Схема сервиса inventory (база inventory): копия единиц компонентов крови из АИСТ-М (O01), история статусов,
-- сеансы обмена с АИСТ-М. Имена — из словаря данных ПР6 (docs/architecture/data-model.md).
-- Платформенные таблицы (outbox, inbox, audit_local, ref_*, в том числе ref_unit_status) создаются миграциями 00001–00099.

-- Единица компонента крови. unit_id — суррогат uuid v7, единственный идентификатор вне inventory;
-- исходный код АИСТ-М хранится для сверки, но в события не публикуется (ADR-0010).
CREATE TABLE unit (
    unit_id             uuid         PRIMARY KEY,
    aist_unit_code      varchar(64)  NOT NULL UNIQUE,
    clinic_id           bigint       NOT NULL REFERENCES ref_clinic (clinic_id),
    storage_location    varchar(128) NOT NULL DEFAULT '',
    component_kind_code varchar(16)  NOT NULL REFERENCES ref_component_kind (component_kind_code),
    volume_ml           bigint       NOT NULL CHECK (volume_ml >= 0),
    blood_group_code    varchar(4)   NOT NULL REFERENCES ref_blood_group (blood_group_code),
    phenotype           varchar(64),
    expires_at          timestamptz  NOT NULL,
    unit_status_code    varchar(16)  NOT NULL REFERENCES ref_unit_status (unit_status_code),
    status_received_at  timestamptz  NOT NULL,
    source_version      bigint       NOT NULL CHECK (source_version > 0)
);
COMMENT ON TABLE unit IS 'Копия единиц компонентов крови из АИСТ-М (O01); строки не удаляются: прослеживаемость, ПП РФ № 641';
COMMENT ON COLUMN unit.aist_unit_code IS 'Исходный код единицы в АИСТ-М; наружу не публикуется (ADR-0010)';
COMMENT ON COLUMN unit.source_version IS 'Версия записи в АИСТ-М: старая версия не перезаписывает новую';
CREATE INDEX unit_stock_idx ON unit (clinic_id, component_kind_code, blood_group_code, expires_at);

-- История статусов единицы: строка на каждую смену статуса, повтор версии упирается в уникальность.
CREATE TABLE unit_status_history (
    unit_status_history_id uuid        PRIMARY KEY,
    unit_id                uuid        NOT NULL REFERENCES unit (unit_id),
    unit_status_code       varchar(16) NOT NULL REFERENCES ref_unit_status (unit_status_code),
    source_version         bigint      NOT NULL,
    changed_at             timestamptz NOT NULL,
    received_at            timestamptz NOT NULL,
    UNIQUE (unit_id, source_version)
);

-- Сеанс обмена с АИСТ-М (ИНТ-01). Время окончания последнего успешного сеанса CHANGES — водяной знак копии.
CREATE TABLE sync_run (
    sync_run_id    uuid        PRIMARY KEY,
    sync_kind      varchar(16) NOT NULL CHECK (sync_kind IN ('CHANGES', 'DAILY_SNAPSHOT', 'ON_DEMAND', 'UNIT_CHECK')),
    requested_by   varchar(64) NOT NULL,
    started_at     timestamptz NOT NULL,
    finished_at    timestamptz,
    sync_result    varchar(16) CHECK (sync_result IN ('SUCCESS', 'PARTIAL', 'FAILED')),
    changes_count  integer     NOT NULL DEFAULT 0 CHECK (changes_count >= 0),
    error_text     text,
    source_version bigint      NOT NULL DEFAULT 0 CHECK (source_version >= 0),
    CHECK ((finished_at IS NULL) = (sync_result IS NULL))
);
COMMENT ON COLUMN sync_run.source_version IS 'Курсор опроса изменений: последняя применённая версия АИСТ-М (last_version)';
CREATE INDEX sync_run_cursor_idx ON sync_run (sync_kind, started_at DESC, sync_run_id DESC);
CREATE INDEX sync_run_watermark_idx ON sync_run (sync_kind, finished_at DESC) WHERE sync_result = 'SUCCESS';

-- Версия защищённого объёма для демо-публикации до появления planning: растёт только вперёд.
CREATE SEQUENCE protected_volume_version_seq;

-- Строки единиц и истории статусов не удаляются: прослеживаемость единицы до конечного использования (ПП РФ № 641).
-- +goose StatementBegin
CREATE FUNCTION unit_no_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% в % запрещено: прослеживаемость единицы (ПП РФ № 641)', TG_OP, TG_TABLE_NAME;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER unit_no_delete BEFORE DELETE ON unit
    FOR EACH ROW EXECUTE FUNCTION unit_no_delete();
CREATE TRIGGER unit_no_truncate BEFORE TRUNCATE ON unit
    FOR EACH STATEMENT EXECUTE FUNCTION unit_no_delete();
CREATE TRIGGER unit_status_history_no_change BEFORE UPDATE OR DELETE ON unit_status_history
    FOR EACH ROW EXECUTE FUNCTION unit_no_delete();
CREATE TRIGGER unit_status_history_no_truncate BEFORE TRUNCATE ON unit_status_history
    FOR EACH STATEMENT EXECUTE FUNCTION unit_no_delete();

-- +goose Down
DROP TRIGGER unit_status_history_no_truncate ON unit_status_history;
DROP TRIGGER unit_status_history_no_change ON unit_status_history;
DROP TRIGGER unit_no_truncate ON unit;
DROP TRIGGER unit_no_delete ON unit;
DROP FUNCTION unit_no_delete();
DROP SEQUENCE protected_volume_version_seq;
DROP TABLE sync_run;
DROP TABLE unit_status_history;
DROP TABLE unit;
