-- +goose Up
ALTER TABLE transport_request ADD COLUMN progress_checked_at timestamptz;
ALTER TABLE transport_request ADD COLUMN dispatch_reported boolean NOT NULL DEFAULT false;
ALTER TABLE transport_request ADD COLUMN delivery_reported boolean NOT NULL DEFAULT false;
ALTER TABLE transport_request ADD COLUMN progress_payload jsonb;
CREATE INDEX transport_progress_due_idx ON transport_request(progress_checked_at) WHERE status='ACCEPTED' AND NOT delivery_reported;
-- +goose Down
DROP INDEX transport_progress_due_idx;
ALTER TABLE transport_request DROP COLUMN progress_checked_at,DROP COLUMN dispatch_reported,DROP COLUMN delivery_reported,DROP COLUMN progress_payload;
