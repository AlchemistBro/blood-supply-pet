-- +goose Up
CREATE TABLE aist_outbox_message (
    id bigserial PRIMARY KEY,
    idempotency_key uuid NOT NULL UNIQUE,
    obligation_id uuid NOT NULL,
    message_type text NOT NULL CHECK(message_type IN ('AllocateUnitsToPatient','ReleasePatientAllocation')),
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL,
    lease_owner uuid,
    lease_until timestamptz,
    acknowledged_at timestamptz,
    released_at timestamptz
);
CREATE INDEX aist_pending ON aist_outbox_message(next_attempt_at,id) WHERE acknowledged_at IS NULL;
CREATE INDEX aist_obligation ON aist_outbox_message(obligation_id,id);

-- +goose Down
DROP TABLE aist_outbox_message;
