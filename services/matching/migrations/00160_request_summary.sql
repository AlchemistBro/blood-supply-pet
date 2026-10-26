-- +goose Up
-- Keep the list projection inline: subtracting Variants on every read still
-- fetches and decompresses the entire TOASTed matching result for 100 requests.
ALTER TABLE clinical_request ADD COLUMN match_summary jsonb
    GENERATED ALWAYS AS (match_result - 'Variants') STORED NOT NULL;

-- +goose Down
ALTER TABLE clinical_request DROP COLUMN match_summary;
