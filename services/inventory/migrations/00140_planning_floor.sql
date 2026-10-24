-- +goose Up
CREATE TABLE planning_floor_version (
    clinic_id bigint NOT NULL,
    component_kind text NOT NULL,
    version bigint NOT NULL,
    PRIMARY KEY (clinic_id, component_kind)
);

-- +goose Down
DROP TABLE planning_floor_version;
