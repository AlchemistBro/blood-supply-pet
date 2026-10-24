-- +goose Up
ALTER TABLE demand_estimate ADD COLUMN rejection_reason text NOT NULL DEFAULT '';
ALTER TABLE demand_estimate ADD COLUMN rejected_by text;
ALTER TABLE demand_estimate ADD COLUMN rejected_at timestamptz;
-- +goose Down
ALTER TABLE demand_estimate DROP COLUMN rejection_reason,DROP COLUMN rejected_by,DROP COLUMN rejected_at;
