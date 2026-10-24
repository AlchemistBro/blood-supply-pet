
-- name: InsertObligation :execrows
-- Повтор choice_id ничего не вставляет: идемпотентность открытия задания.
INSERT INTO obligation (
    obligation_id, choice_id, request_id, origin, source_clinic_id, target_clinic_id, component_kind_code,
    blood_group_code, requested_qty, status, urgency_code, deadline, confirm_window_seconds, fallback_sources,
    spec_blood_group_code, spec_rh_positive, spec_phenotype, spec_min_expires_at, spec_rule_version,
    created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, 'PENDING', $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $19
)
ON CONFLICT (choice_id) DO NOTHING;

-- name: ObligationByID :one
SELECT * FROM obligation WHERE obligation_id = $1;

-- name: LockObligation :one
-- Вторая блокировка в порядке «Остаток, затем O07».
SELECT * FROM obligation WHERE obligation_id = $1 FOR UPDATE;

-- name: ObligationIDByChoice :one
SELECT obligation_id FROM obligation WHERE choice_id = $1;

-- name: ObligationsByRequest :many
SELECT * FROM obligation WHERE request_id = $1 ORDER BY created_at, obligation_id;

-- name: PendingDueBy :many
SELECT obligation_id FROM obligation
WHERE status = 'PENDING' AND deadline <= sqlc.arg('now')
ORDER BY deadline
LIMIT sqlc.arg('max_rows');

-- name: CASStatus :execrows
UPDATE obligation
SET status = sqlc.arg('next'), status_reason = COALESCE(sqlc.narg('reason'), status_reason),
    version = version + 1, updated_at = sqlc.arg('now')
WHERE obligation_id = sqlc.arg('obligation_id') AND status = sqlc.arg('expected') AND version = sqlc.arg('version');

-- name: CASConfirm :execrows
UPDATE obligation
SET status = 'CONFIRMED', confirmed_qty = sqlc.arg('confirmed_qty'), confirm_key = sqlc.arg('confirm_key'),
    version = version + 1, updated_at = sqlc.arg('now')
WHERE obligation_id = sqlc.arg('obligation_id') AND status = 'PENDING' AND version = sqlc.arg('version');

-- name: CASTouch :execrows
-- Изменение внутри статуса (замена единицы): версия растёт, статус прежний.
UPDATE obligation SET version = version + 1, updated_at = sqlc.arg('now')
WHERE obligation_id = sqlc.arg('obligation_id') AND status = sqlc.arg('status') AND version = sqlc.arg('version');

-- name: CASTransportRequested :execrows
-- Только из CONFIRMED: нарушенное или отменённое O07 поздним ответом перевозчика не оживает.
UPDATE obligation
SET status = 'TRANSPORT_REQUESTED', transport_ref = sqlc.arg('transport_ref'), version = version + 1, updated_at = sqlc.arg('now')
WHERE obligation_id = sqlc.arg('obligation_id') AND status = 'CONFIRMED';

-- name: MarkEscalated :execrows
-- Одна эскалация «АИСТ-М недоступна» на задание, пока оно PENDING; статус и версия не меняются.
UPDATE obligation SET escalated_at = sqlc.arg('now')
WHERE obligation_id = sqlc.arg('obligation_id') AND status = 'PENDING' AND escalated_at IS NULL;

-- name: UrgencyWindowMinutes :one
SELECT confirm_window_minutes FROM ref_urgency WHERE urgency_code = $1;


-- name: EnsureStock :exec
INSERT INTO source_stock (clinic_id, component_kind_code) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: LockStock :one
-- Первая блокировка любой операции над единицами источника (NFR013).
SELECT available_units, reserved_units, protected_volume FROM source_stock
WHERE clinic_id = $1 AND component_kind_code = $2
FOR UPDATE;

-- name: AddReserved :exec
UPDATE source_stock SET reserved_units = reserved_units + sqlc.arg('delta')
WHERE clinic_id = sqlc.arg('clinic_id') AND component_kind_code = sqlc.arg('component_kind_code');

-- name: RecountAvailable :exec
-- Пересчёт по копии вместо инкремента: проекция переживает дубли и перестановку событий.
UPDATE source_stock SET available_units = (
    SELECT count(*) FROM unit_copy u JOIN ref_unit_status s USING (unit_status_code)
    WHERE u.clinic_id = sqlc.arg('clinic_id') AND u.component_kind_code = sqlc.arg('component_kind_code') AND s.is_available)
WHERE source_stock.clinic_id = sqlc.arg('clinic_id') AND source_stock.component_kind_code = sqlc.arg('component_kind_code');

-- name: SetProtectedVolume :execrows
UPDATE source_stock
SET protected_volume = sqlc.arg('protected_volume'), protected_version = sqlc.arg('protected_version'),
    protected_reason = sqlc.narg('protected_reason')
WHERE clinic_id = sqlc.arg('clinic_id') AND component_kind_code = sqlc.arg('component_kind_code')
  AND protected_version < sqlc.arg('protected_version');


-- name: UpsertUnitCopy :execrows
INSERT INTO unit_copy (unit_id, clinic_id, component_kind_code, blood_group_code, rh_positive, phenotype, volume_ml,
                       expires_at, unit_status_code, status_reason, version, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULL, $10, $11)
ON CONFLICT (unit_id) DO UPDATE
SET clinic_id = excluded.clinic_id, component_kind_code = excluded.component_kind_code,
    blood_group_code = excluded.blood_group_code, rh_positive = excluded.rh_positive, phenotype = excluded.phenotype,
    volume_ml = excluded.volume_ml, expires_at = excluded.expires_at, unit_status_code = excluded.unit_status_code,
    status_reason = NULL, version = excluded.version, updated_at = excluded.updated_at
WHERE unit_copy.version < excluded.version;

-- name: SetUnitCopyStatus :execrows
-- Устаревшая или повторная версия события ничего не меняет.
UPDATE unit_copy
SET unit_status_code = sqlc.arg('unit_status_code'), status_reason = sqlc.narg('status_reason'), version = sqlc.arg('version'),
    updated_at = sqlc.arg('now')
WHERE unit_id = sqlc.arg('unit_id') AND version < sqlc.arg('version');

-- name: UnitCopies :many
SELECT * FROM unit_copy WHERE unit_id = ANY(sqlc.arg('unit_ids')::uuid[]);

-- name: NotInStockAmong :many
-- Недоступные по справочнику ref_unit_status: карантин и выбывшие.
SELECT u.unit_id FROM unit_copy u JOIN ref_unit_status s USING (unit_status_code)
WHERE u.unit_id = ANY(sqlc.arg('unit_ids')::uuid[]) AND NOT s.is_available;


-- name: InsertAllocation :exec
-- UNIQUE (unit_id) WHERE status = 'CONFIRMED' (NFR004): проигравшая транзакция получает 23505 и откатывается.
INSERT INTO allocation (allocation_id, unit_id, obligation_id, status, created_at)
VALUES ($1, $2, $3, 'CONFIRMED', $4);

-- name: ReleaseAllocation :execrows
UPDATE allocation SET status = 'RELEASED', closed_at = sqlc.arg('now')::timestamptz
WHERE obligation_id = sqlc.arg('obligation_id') AND unit_id = sqlc.arg('unit_id') AND status = 'CONFIRMED';

-- name: ReleaseAllAllocations :execrows
UPDATE allocation SET status = 'RELEASED', closed_at = sqlc.arg('now')::timestamptz
WHERE obligation_id = sqlc.arg('obligation_id') AND status = 'CONFIRMED';

-- name: ConfirmedUnits :many
SELECT unit_id FROM allocation
WHERE obligation_id = $1 AND status = 'CONFIRMED'
ORDER BY created_at, allocation_id;

-- name: ConfirmedAmong :many
SELECT unit_id FROM allocation WHERE status = 'CONFIRMED' AND unit_id = ANY(sqlc.arg('unit_ids')::uuid[]);

-- name: ObligationHoldingUnit :one
SELECT obligation_id FROM allocation WHERE unit_id = $1 AND status = 'CONFIRMED';

-- name: IsAllocatedTo :one
SELECT EXISTS (
    SELECT 1 FROM allocation WHERE obligation_id = $1 AND unit_id = $2 AND status = 'CONFIRMED'
) AS allocated;

-- name: AllocationHistory :many
-- Куда уходила единица: все закрепления, включая снятые.
SELECT * FROM allocation WHERE unit_id = $1 ORDER BY created_at, allocation_id;

-- name: ObligationsBySource :many
-- Задания клиники-источника для экрана оператора; статус — необязательный фильтр.
SELECT * FROM obligation
WHERE source_clinic_id = sqlc.arg('source_clinic_id')
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
ORDER BY created_at DESC, obligation_id
LIMIT sqlc.arg('max_rows');

-- name: UnknownClinics :many
-- Клиники из списка, которых нет в справочнике: проверка источника, получателя и fallback_sources при открытии.
SELECT c::bigint AS clinic_id FROM unnest(sqlc.arg('clinic_ids')::bigint[]) AS c
WHERE NOT EXISTS (SELECT 1 FROM ref_clinic r WHERE r.clinic_id = c);
