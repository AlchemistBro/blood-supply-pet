-- +goose Up
CREATE TABLE cancelled_request (
    request_id uuid PRIMARY KEY,
    reason text NOT NULL,
    cancelled_at timestamptz NOT NULL
);

-- +goose Down
DROP TABLE cancelled_request;
