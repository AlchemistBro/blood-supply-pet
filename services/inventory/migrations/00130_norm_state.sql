-- +goose Up
-- Версии нормативов и переходы через порог (FR012–FR014); блокировка строки исключает дубли события.
CREATE SEQUENCE stock_norm_version_seq;
ALTER TABLE stock_norm ADD COLUMN version bigint NOT NULL DEFAULT nextval('stock_norm_version_seq');
CREATE TABLE stock_norm_state (
    clinic_id bigint NOT NULL REFERENCES ref_clinic(clinic_id),
    component_kind_code varchar(16) NOT NULL REFERENCES ref_component_kind(component_kind_code),
    below boolean NOT NULL DEFAULT false,
    PRIMARY KEY(clinic_id,component_kind_code)
);
-- +goose Down
DROP TABLE stock_norm_state;
ALTER TABLE stock_norm DROP COLUMN version;
DROP SEQUENCE stock_norm_version_seq;
