-- +goose Up
CREATE INDEX matching_request_projection ON matching_input_projection((payload->>'request_id')) WHERE input_key LIKE 'obligation:%';

-- +goose Down
DROP INDEX matching_request_projection;
