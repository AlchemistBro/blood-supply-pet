-- +goose Up
ALTER TABLE replenishment_plan ADD COLUMN superseded boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE replenishment_plan DROP COLUMN superseded;
