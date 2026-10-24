-- +goose Up
CREATE TABLE request_status_dict(status_code text PRIMARY KEY);
INSERT INTO request_status_dict VALUES ('OFFERS_READY'),('DEFICIT'),('LOCAL_SELECTED'),('TASK_OPENED'),('CONFIRMED'),('TRANSPORT_REQUESTED'),('DISPATCHED'),('DELIVERED'),('CANCELLED');
CREATE TABLE matching_rule (
    matching_rule_id uuid PRIMARY KEY,
    rule_code text NOT NULL,
    version_no bigint NOT NULL,
    valid_from timestamptz NOT NULL,
    valid_to timestamptz,
    payload jsonb NOT NULL,
    UNIQUE(rule_code,version_no)
);
CREATE TABLE clinical_request (
    clinical_request_id uuid PRIMARY KEY,
    clinic_id bigint NOT NULL REFERENCES ref_clinic(clinic_id),
    created_by text NOT NULL,
    request_status_code text NOT NULL REFERENCES request_status_dict(status_code),
    payload jsonb NOT NULL,
    match_result jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1
);
CREATE INDEX clinical_request_clinic ON clinical_request(clinic_id,created_at DESC);
CREATE TABLE clinical_request_patient (
    clinical_request_id uuid PRIMARY KEY REFERENCES clinical_request(clinical_request_id),
    patient_case_sealed text NOT NULL
);
CREATE TABLE request_constraint (
    clinical_request_id uuid NOT NULL REFERENCES clinical_request(clinical_request_id),
    constraint_code text NOT NULL,
    constraint_value text NOT NULL,
    PRIMARY KEY(clinical_request_id,constraint_code)
);
CREATE TABLE variant_offer (
    variant_offer_id uuid PRIMARY KEY,
    clinical_request_id uuid NOT NULL REFERENCES clinical_request(clinical_request_id),
    rank_no integer NOT NULL,
    payload jsonb NOT NULL,
    UNIQUE(clinical_request_id,rank_no)
);
CREATE TABLE variant_offer_unit_link (
    variant_offer_id uuid NOT NULL REFERENCES variant_offer(variant_offer_id),
    unit_id uuid NOT NULL,
    PRIMARY KEY(variant_offer_id,unit_id)
);
CREATE TABLE variant_selection (
    clinical_request_id uuid PRIMARY KEY REFERENCES clinical_request(clinical_request_id),
    variant_offer_id uuid NOT NULL REFERENCES variant_offer(variant_offer_id),
    choice_id uuid NOT NULL UNIQUE,
    selected_by text NOT NULL,
    selected_at timestamptz NOT NULL
);
CREATE TABLE matching_input_projection (
    input_key text PRIMARY KEY,
    source_version bigint NOT NULL,
    occurred_at timestamptz NOT NULL,
    payload jsonb NOT NULL
);

-- +goose Down
DROP TABLE matching_input_projection;
DROP TABLE variant_selection;
DROP TABLE variant_offer_unit_link;
DROP TABLE variant_offer;
DROP TABLE request_constraint;
DROP TABLE clinical_request_patient;
DROP TABLE clinical_request;
DROP TABLE matching_rule;
DROP TABLE request_status_dict;
