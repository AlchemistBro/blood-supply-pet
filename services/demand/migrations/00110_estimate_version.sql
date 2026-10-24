-- +goose Up
CREATE SEQUENCE demand_estimate_version_seq;
SELECT setval('demand_estimate_version_seq',COALESCE((SELECT max(version_no)+1 FROM demand_estimate),1),false);
-- +goose Down
DROP SEQUENCE demand_estimate_version_seq;
