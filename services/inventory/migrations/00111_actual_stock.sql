-- +goose Up
-- Фактический запас (O02, FR010, FR011): только единицы в доступном статусе по справочнику ref_unit_status
-- (карантин недоступен, ПП РФ № 641, п. 62) и с неистёкшим сроком годности (FR007).
-- Момент годности — параметр: приложение передаёт время своих часов (Clock), а не now() базы,
-- поэтому тесты сроков воспроизводимы. Представление actual_stock — тот же расчёт на now() для ручных запросов.

-- Единицы фактического запаса на момент p_at. Простая SQL-функция STABLE встраивается планировщиком:
-- условия по клинике и сроку из внешнего запроса доходят до индекса.
-- +goose StatementBegin
CREATE FUNCTION available_unit_at(p_at timestamptz) RETURNS SETOF unit
LANGUAGE sql STABLE AS $$
    SELECT u.*
    FROM unit u
    JOIN ref_unit_status s ON s.unit_status_code = u.unit_status_code
    WHERE s.is_available AND u.expires_at > p_at
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION actual_stock_at(p_at timestamptz)
RETURNS TABLE (
    clinic_id           bigint,
    component_kind_code varchar,
    blood_group_code    varchar,
    unit_count          bigint,
    volume_ml           bigint,
    nearest_expiry      timestamptz
)
LANGUAGE sql STABLE AS $$
    SELECT a.clinic_id, a.component_kind_code, a.blood_group_code,
           count(*), sum(a.volume_ml)::bigint, min(a.expires_at)
    FROM available_unit_at(p_at) a
    GROUP BY a.clinic_id, a.component_kind_code, a.blood_group_code
$$;
-- +goose StatementEnd

CREATE VIEW actual_stock AS
    SELECT clinic_id, component_kind_code, blood_group_code, unit_count, volume_ml, nearest_expiry
    FROM actual_stock_at(now());
COMMENT ON VIEW actual_stock IS 'Фактический запас O02 на текущий момент: доступные по ref_unit_status и неистёкшие единицы';

-- Индекс под фактический запас и FEFO по клинике: отбор по клинике, порядок по годности (FR015).
CREATE INDEX unit_clinic_fefo_idx ON unit (clinic_id, expires_at, unit_id);

-- +goose Down
DROP INDEX unit_clinic_fefo_idx;
DROP VIEW actual_stock;
DROP FUNCTION actual_stock_at(timestamptz);
DROP FUNCTION available_unit_at(timestamptz);
