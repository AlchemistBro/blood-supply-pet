-- +goose Up
CREATE TABLE planning_projection_journal (
    seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    input_key text NOT NULL,
    source_version bigint NOT NULL,
    occurred_at timestamptz NOT NULL,
    payload jsonb NOT NULL
);
CREATE INDEX planning_projection_journal_latest_idx ON planning_projection_journal(input_key,seq DESC);
INSERT INTO planning_projection_journal(input_key,source_version,occurred_at,payload)
SELECT input_key,source_version,occurred_at,payload FROM planning_input_projection ORDER BY input_key;
CREATE INDEX planning_obligation_request_idx ON planning_input_projection((payload->>'request_id')) WHERE input_key LIKE 'obligation:%';
-- +goose Down
DROP INDEX planning_obligation_request_idx;
DROP TABLE planning_projection_journal;
