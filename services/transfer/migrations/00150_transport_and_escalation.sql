-- +goose Up
ALTER TABLE unit_copy ADD COLUMN local_dispatch_obligation_id uuid REFERENCES obligation(obligation_id);
ALTER TABLE unit_copy ADD COLUMN clinical_use_blocked boolean NOT NULL DEFAULT false;
ALTER TABLE obligation ADD COLUMN clinical_use_blocked boolean NOT NULL DEFAULT false;
ALTER TABLE obligation ADD COLUMN transport_violations text[] NOT NULL DEFAULT '{}';
CREATE TABLE obligation_transport (
    obligation_id uuid PRIMARY KEY REFERENCES obligation(obligation_id),
    payload jsonb NOT NULL
);
CREATE TABLE escalation (
    escalation_id uuid PRIMARY KEY,
    obligation_id uuid NOT NULL REFERENCES obligation(obligation_id),
    reason text NOT NULL,
    raised_at timestamptz NOT NULL,
    closed_at timestamptz,
    closed_by text,
    resolution text,
    comment text,
    UNIQUE(obligation_id,reason)
);
CREATE INDEX escalation_open ON escalation(raised_at,escalation_id) WHERE closed_at IS NULL;
INSERT INTO escalation(escalation_id,obligation_id,reason,raised_at)
SELECT DISTINCT ON (o.payload->>'obligation_id',o.payload->>'reason')
       o.message_id,ob.obligation_id,o.payload->>'reason',o.occurred_at
FROM outbox o JOIN obligation ob ON ob.obligation_id=(o.payload->>'obligation_id')::uuid
WHERE o.message_type='EscalationRaised'
ORDER BY o.payload->>'obligation_id',o.payload->>'reason',o.occurred_at DESC
ON CONFLICT DO NOTHING;

-- +goose Down
DROP TABLE escalation;
DROP TABLE obligation_transport;
ALTER TABLE obligation DROP COLUMN transport_violations;
ALTER TABLE obligation DROP COLUMN clinical_use_blocked;
ALTER TABLE unit_copy DROP COLUMN clinical_use_blocked;
ALTER TABLE unit_copy DROP COLUMN local_dispatch_obligation_id;
