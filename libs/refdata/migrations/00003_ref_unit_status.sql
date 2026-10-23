-- +goose Up
-- Справочник статусов единицы компонента крови (unit_status_dict ПР6) в базе каждого сервиса, данные из libs/refdata.
-- is_available — единица может быть закреплена и передана. Карантин недоступен: ПП РФ № 641, п. 62.

CREATE TABLE ref_unit_status (
    unit_status_code varchar(16) PRIMARY KEY,
    unit_status_name varchar(64) NOT NULL UNIQUE,
    is_available     boolean     NOT NULL
);
COMMENT ON TABLE ref_unit_status IS 'Статус единицы компонента крови по данным АИСТ-М (C01)';
COMMENT ON COLUMN ref_unit_status.is_available IS 'Единица в наличии и может быть закреплена (O02 actual_stock)';

-- +goose Down
DROP TABLE ref_unit_status;
