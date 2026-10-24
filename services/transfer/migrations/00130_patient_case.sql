-- +goose Up
ALTER TABLE obligation ADD COLUMN patient_case_sealed text;
ALTER TABLE obligation ADD COLUMN patient_allocation_key uuid;
ALTER TABLE obligation ADD COLUMN patient_allocation_status text NOT NULL DEFAULT 'NOT_REQUIRED';
ALTER TABLE obligation ADD COLUMN patient_allocation_at timestamptz;

-- +goose Down
ALTER TABLE obligation DROP COLUMN patient_case_sealed;
ALTER TABLE obligation DROP COLUMN patient_allocation_key;
ALTER TABLE obligation DROP COLUMN patient_allocation_status;
ALTER TABLE obligation DROP COLUMN patient_allocation_at;
