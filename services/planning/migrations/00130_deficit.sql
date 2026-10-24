-- +goose Up
CREATE TABLE deficit_record (
    deficit_record_id uuid PRIMARY KEY,
    record_type text NOT NULL CHECK(record_type IN ('PLAN','REQUEST')),
    origin_id uuid NOT NULL,
    clinic_id bigint NOT NULL,
    component_kind text NOT NULL,
    blood_group text NOT NULL,
    uncovered_ml bigint NOT NULL DEFAULT 0,
    uncovered_units integer NOT NULL DEFAULT 0,
    reason text NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX deficit_filter ON deficit_record(clinic_id,record_type,created_at DESC);

-- +goose Down
DROP TABLE deficit_record;
