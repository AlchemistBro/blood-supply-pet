-- +goose Up
-- Версионные копии входов; JSON содержит только белые списки полей DTO planning, без K1 и фенотипа.
CREATE TABLE planning_input_projection (
    input_key text PRIMARY KEY,
    source_version bigint NOT NULL,
    occurred_at timestamptz NOT NULL,
    payload jsonb NOT NULL
);
CREATE TABLE emergency_forecast (
    emergency_forecast_id uuid PRIMARY KEY,
    scenario_version text NOT NULL UNIQUE,
    payload jsonb NOT NULL,
    received_at timestamptz NOT NULL
);
CREATE SEQUENCE replenishment_plan_version_seq;
CREATE TABLE replenishment_plan (
    replenishment_plan_id uuid PRIMARY KEY,
    demand_estimate_id uuid NOT NULL,
    emergency_forecast_id uuid REFERENCES emergency_forecast(emergency_forecast_id),
    stock_watermark bigint NOT NULL,
    stock_synced_at timestamptz,
    coverage_ratio numeric(5,4) NOT NULL,
    elapsed_ms bigint NOT NULL,
    created_at timestamptz NOT NULL,
    input_snapshot jsonb NOT NULL,
    result jsonb NOT NULL
);
-- +goose Down
DROP TABLE replenishment_plan;
DROP SEQUENCE replenishment_plan_version_seq;
DROP TABLE emergency_forecast;
DROP TABLE planning_input_projection;
