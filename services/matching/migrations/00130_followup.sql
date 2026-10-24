-- +goose Up
CREATE TABLE matching_followup (
    obligation_id uuid NOT NULL,
    obligation_version bigint NOT NULL,
    event_type text NOT NULL,
    PRIMARY KEY(obligation_id,obligation_version,event_type)
);

-- +goose Down
DROP TABLE matching_followup;
