-- +goose Up
-- Разрезы сохраняются после переноса единицы: следующий снимок публикует нулевой остаток старого разреза.
CREATE TABLE stock_snapshot_key (
    clinic_id bigint NOT NULL REFERENCES ref_clinic(clinic_id),
    component_kind_code varchar(16) NOT NULL REFERENCES ref_component_kind(component_kind_code),
    blood_group_code varchar(8) NOT NULL REFERENCES ref_blood_group(blood_group_code),
    PRIMARY KEY(clinic_id,component_kind_code,blood_group_code)
);
-- +goose Down
DROP TABLE stock_snapshot_key;
