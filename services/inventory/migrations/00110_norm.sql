-- +goose Up
-- Годовой расход и норматив запаса (O03, FR012, FR013). Формулы и ввод — следующий пакет этапа 3;
-- здесь только таблицы по словарю ПР6 (docs/architecture/data-model.md).

-- Годовой расход Kгод, введённый оператором клиники. Строки не изменяются: новая версия — новая строка.
CREATE TABLE annual_consumption (
    annual_consumption_id uuid        PRIMARY KEY,
    clinic_id             bigint      NOT NULL REFERENCES ref_clinic (clinic_id),
    component_kind_code   varchar(16) NOT NULL REFERENCES ref_component_kind (component_kind_code),
    consumption_year      smallint    NOT NULL CHECK (consumption_year BETWEEN 2000 AND 2100),
    annual_volume_ml      bigint      NOT NULL CHECK (annual_volume_ml >= 0),
    entered_by            varchar(64) NOT NULL,
    entered_at            timestamptz NOT NULL
);
COMMENT ON COLUMN annual_consumption.entered_by IS 'Пользователь из токена (без ФИО)';
CREATE INDEX annual_consumption_clinic_kind_idx ON annual_consumption (clinic_id, component_kind_code, entered_at DESC);

-- Норматив запаса: Kср = Kгод / 12, N = Kгод / 360, не меньше одной единицы (FR012). Действующий — один на клинику и вид.
CREATE TABLE stock_norm (
    stock_norm_id         uuid          PRIMARY KEY,
    annual_consumption_id uuid          NOT NULL REFERENCES annual_consumption (annual_consumption_id),
    clinic_id             bigint        NOT NULL REFERENCES ref_clinic (clinic_id),
    component_kind_code   varchar(16)   NOT NULL REFERENCES ref_component_kind (component_kind_code),
    avg_monthly_ml        numeric(14,3) NOT NULL CHECK (avg_monthly_ml >= 0),
    norm_ml               numeric(14,3) NOT NULL CHECK (norm_ml >= 0),
    is_min_unit_applied   boolean       NOT NULL,
    is_current            boolean       NOT NULL,
    calculated_at         timestamptz   NOT NULL
);
CREATE UNIQUE INDEX stock_norm_current_uq ON stock_norm (clinic_id, component_kind_code) WHERE is_current;
CREATE INDEX stock_norm_history_idx ON stock_norm (clinic_id, component_kind_code, calculated_at DESC);

-- +goose Down
DROP TABLE stock_norm;
DROP TABLE annual_consumption;
