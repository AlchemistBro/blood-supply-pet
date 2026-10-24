-- +goose Up
CREATE TABLE security_incident (
    incident_id uuid PRIMARY KEY,
    rule text NOT NULL,
    user_id text NOT NULL,
    count integer NOT NULL,
    window_seconds integer NOT NULL,
    first_seen_at timestamptz NOT NULL,
    detected_at timestamptz NOT NULL,
    audit_seq bigint NOT NULL
);
CREATE INDEX security_incident_suppression_idx ON security_incident(rule, user_id, detected_at DESC);
CREATE INDEX audit_record_detection_idx ON audit_record(received_at, user_id, action, result);
-- +goose Down
DROP INDEX audit_record_detection_idx;
DROP TABLE security_incident;
