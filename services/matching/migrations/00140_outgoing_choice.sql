-- +goose Up
CREATE TABLE matching_outgoing_choice (
    choice_id uuid PRIMARY KEY,
    clinical_request_id uuid NOT NULL REFERENCES clinical_request(clinical_request_id),
    source_clinic_id bigint NOT NULL,
    requested_qty integer NOT NULL CHECK(requested_qty>0),
    UNIQUE(clinical_request_id,source_clinic_id)
);

-- +goose Down
DROP TABLE matching_outgoing_choice;
