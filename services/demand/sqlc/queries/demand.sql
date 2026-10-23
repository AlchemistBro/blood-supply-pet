-- name: LockUploads :exec
-- Загрузки одного вида файла идут по очереди: номер версии долей и проверка периодов истории
-- против уже сохранённых не должны гоняться.
SELECT pg_advisory_xact_lock(hashtext(sqlc.arg(lock_key)::text));

-- name: InsertStatisticsFile :one
INSERT INTO statistics_file (
    statistics_file_id, file_kind, file_name, file_checksum, data_source, period_start, period_end,
    check_status, rejected_rows, uploaded_by, uploaded_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
)
ON CONFLICT (file_checksum) DO NOTHING
RETURNING statistics_file_id;

-- name: FileIDByChecksum :one
SELECT statistics_file_id FROM statistics_file WHERE file_checksum = $1;

-- name: InsertFileRowErrors :copyfrom
INSERT INTO file_row_error (statistics_file_id, row_no, error_reason) VALUES ($1, $2, $3);

-- name: InsertDistrictProfiles :copyfrom
INSERT INTO district_profile (district_profile_id, statistics_file_id, district_id, population) VALUES ($1, $2, $3, $4);

-- name: InsertDistrictGroupShares :copyfrom
INSERT INTO district_group_share (district_profile_id, blood_group_code, group_share) VALUES ($1, $2, $3);

-- name: InsertConsumptionHistory :copyfrom
INSERT INTO consumption_history (
    consumption_history_id, statistics_file_id, history_source, component_kind_code, period_start, period_end, consumed_volume_ml
) VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: HistoryPeriods :many
SELECT h.component_kind_code, h.period_start, h.period_end, h.history_source, f.file_name
FROM consumption_history h
LEFT JOIN statistics_file f ON f.statistics_file_id = h.statistics_file_id
WHERE h.history_source = sqlc.arg(history_source);

-- name: NextClinicShareVersionNo :one
SELECT (COALESCE(MAX(version_no), 0) + 1)::bigint AS version_no FROM clinic_share_version;

-- name: InsertClinicShareVersion :exec
INSERT INTO clinic_share_version (clinic_share_version_id, version_no, statistics_file_id, basis, valid_from)
VALUES ($1, $2, $3, $4, $5);

-- name: InsertClinicShares :copyfrom
INSERT INTO clinic_share (clinic_share_version_id, district_id, clinic_id, clinic_share) VALUES ($1, $2, $3, $4);

-- name: ListFiles :many
SELECT statistics_file_id, file_kind, file_name, file_checksum, data_source, period_start, period_end,
       check_status, rejected_rows, uploaded_by, uploaded_at
FROM statistics_file
WHERE (sqlc.narg('file_kind')::text IS NULL OR file_kind = sqlc.narg('file_kind'))
ORDER BY uploaded_at DESC, statistics_file_id DESC
LIMIT sqlc.arg('page_limit');

-- name: GetFile :one
SELECT statistics_file_id, file_kind, file_name, file_checksum, data_source, period_start, period_end,
       check_status, rejected_rows, uploaded_by, uploaded_at
FROM statistics_file
WHERE statistics_file_id = $1;

-- name: ListFileRowErrors :many
SELECT row_no, error_reason FROM file_row_error WHERE statistics_file_id = $1 ORDER BY row_no;

-- name: CountFileRows :one
SELECT (
    (SELECT count(*) FROM district_profile p WHERE p.statistics_file_id = sqlc.arg(file_id))
  + (SELECT count(*) FROM consumption_history h WHERE h.statistics_file_id = sqlc.arg(file_id))
  + (SELECT count(*) FROM clinic_share s
       JOIN clinic_share_version v ON v.clinic_share_version_id = s.clinic_share_version_id
      WHERE v.statistics_file_id = sqlc.arg(file_id))
)::bigint AS accepted_rows;

-- name: ListConsumptionHistory :many
SELECT consumption_history_id, statistics_file_id, history_source, component_kind_code, period_start, period_end, consumed_volume_ml
FROM consumption_history
WHERE (sqlc.narg('component_kind_code')::text IS NULL OR component_kind_code = sqlc.narg('component_kind_code'))
ORDER BY component_kind_code, period_start;

-- name: ListClinicShareVersions :many
SELECT clinic_share_version_id, version_no, statistics_file_id, basis, valid_from
FROM clinic_share_version
ORDER BY version_no DESC;

-- name: CurrentClinicShares :many
SELECT v.clinic_share_version_id, v.version_no, v.statistics_file_id, v.basis, v.valid_from, s.district_id, s.clinic_id, s.clinic_share
FROM clinic_share s
JOIN clinic_share_version v ON v.clinic_share_version_id = s.clinic_share_version_id
WHERE v.version_no = (SELECT MAX(version_no) FROM clinic_share_version)
ORDER BY s.district_id, s.clinic_id;
