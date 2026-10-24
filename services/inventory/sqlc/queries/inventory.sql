-- name: GetUnitByCodeForUpdate :one
-- Строка единицы блокируется до конца транзакции: параллельный опрос применит версию после нас.
SELECT * FROM unit WHERE aist_unit_code = $1 FOR UPDATE;

-- name: InsertUnit :execrows
-- Новая единица; гонку двух вставок одного кода решает уникальность aist_unit_code.
INSERT INTO unit (unit_id, aist_unit_code, clinic_id, storage_location, component_kind_code, volume_ml,
                  blood_group_code, phenotype, expires_at, unit_status_code, status_received_at, source_version)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (aist_unit_code) DO NOTHING;

-- name: UpdateUnitIfNewer :execrows
-- Проверка версии в самом UPDATE: старая или та же версия не перезаписывает новую.
UPDATE unit
SET clinic_id           = @clinic_id,
    storage_location    = @storage_location,
    component_kind_code = @component_kind_code,
    volume_ml           = @volume_ml,
    blood_group_code    = @blood_group_code,
    phenotype           = @phenotype,
    expires_at          = @expires_at,
    unit_status_code    = @unit_status_code,
    status_received_at  = @status_received_at,
    source_version      = @source_version
WHERE unit_id = @unit_id AND source_version < @source_version;

-- name: InsertStatusHistory :exec
INSERT INTO unit_status_history (unit_status_history_id, unit_id, unit_status_code, source_version, changed_at, received_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (unit_id, source_version) DO NOTHING;

-- name: ListUnitsByIDs :many
SELECT * FROM unit WHERE unit_id = ANY(@unit_ids::uuid[]) ORDER BY unit_id;

-- name: GetUnitByCode :one
SELECT * FROM unit WHERE aist_unit_code = $1;

-- name: ListStatusHistory :many
SELECT * FROM unit_status_history WHERE unit_id = $1 ORDER BY source_version;

-- name: StartSyncRun :one
INSERT INTO sync_run (sync_run_id, sync_kind, requested_by, started_at, source_version, clinic_id)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING sync_seq;

-- name: AdvanceSyncRun :exec
-- Курсор и счётчик двигаются в транзакции страницы: обрыв не теряет применённое и не повторяет его зря.
UPDATE sync_run
SET source_version = @source_version,
    changes_count  = changes_count + @changes::bigint
WHERE sync_run_id = @sync_run_id;

-- name: FinishSyncRun :execrows
UPDATE sync_run
SET finished_at         = @finished_at,
    sync_result         = @sync_result,
    error_text          = @error_text,
    discrepancies_count = @discrepancies_count::integer
WHERE sync_run_id = @sync_run_id AND finished_at IS NULL;

-- name: LastSyncCursor :one
-- Курсор — версия последнего сеанса опроса изменений (CHANGES или ON_DEMAND — тот же опрос вне расписания),
-- а не максимум: после перезапуска АИСТ-М курсор сбрасывается в 0.
SELECT source_version FROM sync_run
WHERE sync_kind IN ('CHANGES', 'ON_DEMAND')
ORDER BY started_at DESC, sync_run_id DESC
LIMIT 1;

-- name: LastSuccessfulSync :one
-- Водяной знак копии: окончание последнего успешного сеанса, получившего данные из АИСТ-М
-- (изменения по расписанию, по запросу оператора или суточный снимок, FR001).
SELECT finished_at FROM sync_run
WHERE sync_kind IN ('CHANGES', 'ON_DEMAND', 'DAILY_SNAPSHOT') AND sync_result = 'SUCCESS'
ORDER BY finished_at DESC
LIMIT 1;

-- name: LastDataSyncRun :one
SELECT * FROM sync_run
WHERE sync_kind IN ('CHANGES', 'ON_DEMAND', 'DAILY_SNAPSHOT')
ORDER BY started_at DESC, sync_run_id DESC
LIMIT 1;

-- name: LastSuccessfulDataSyncRun :one
SELECT * FROM sync_run
WHERE sync_kind IN ('CHANGES', 'ON_DEMAND', 'DAILY_SNAPSHOT') AND sync_result = 'SUCCESS'
ORDER BY finished_at DESC, sync_run_id DESC
LIMIT 1;

-- name: CorrectUnitFromSnapshot :execrows
-- Снимок — полный срез АИСТ-М: при той же версии он исправляет копию; старшая версия копии остаётся.
UPDATE unit
SET clinic_id           = @clinic_id,
    storage_location    = @storage_location,
    component_kind_code = @component_kind_code,
    volume_ml           = @volume_ml,
    blood_group_code    = @blood_group_code,
    phenotype           = @phenotype,
    expires_at          = @expires_at,
    unit_status_code    = @unit_status_code,
    status_received_at  = @status_received_at,
    source_version      = @source_version
WHERE unit_id = @unit_id AND source_version <= @source_version;

-- name: ListUnitKeys :many
-- Все единицы копии для поиска тех, кого нет в снимке.
SELECT unit_id, aist_unit_code, clinic_id, component_kind_code, blood_group_code FROM unit;

-- name: GetSyncRun :one
SELECT * FROM sync_run WHERE sync_run_id = $1;

-- name: NextProtectedVolumeVersion :one
SELECT nextval('protected_volume_version_seq')::bigint;

-- name: GetUnit :one
SELECT * FROM unit WHERE unit_id = $1;

-- name: LastSyncRunOfKind :one
SELECT * FROM sync_run
WHERE sync_kind = $1
ORDER BY started_at DESC, sync_run_id DESC
LIMIT 1;

-- name: LastSuccessfulSyncRunOfKind :one
SELECT * FROM sync_run
WHERE sync_kind = $1 AND sync_result = 'SUCCESS'
ORDER BY finished_at DESC, sync_run_id DESC
LIMIT 1;

-- name: ListSyncRuns :many
-- Журнал сеансов обмена, новые первыми; курсор — sync_run_id (uuid v7 растёт со временем создания).
SELECT * FROM sync_run
WHERE (sqlc.narg(sync_kind)::varchar IS NULL OR sync_kind = sqlc.narg(sync_kind)::varchar)
  AND (sqlc.narg(before_id)::uuid IS NULL OR sync_run_id < sqlc.narg(before_id)::uuid)
ORDER BY sync_run_id DESC
LIMIT @row_limit::bigint;
