-- +goose Up
ALTER TABLE obligation ADD COLUMN protected_floors jsonb NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE obligation DROP COLUMN protected_floors;
