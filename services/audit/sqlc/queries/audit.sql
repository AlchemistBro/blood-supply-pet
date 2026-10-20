-- name: LockChainHead :one
SELECT seq, hash FROM audit_chain_head WHERE id = 1 FOR UPDATE;

-- name: UpdateChainHead :exec
UPDATE audit_chain_head SET seq = $1, hash = $2 WHERE id = 1;

-- name: InsertRecord :exec
INSERT INTO audit_record (
    audit_record_id, seq, occurred_at, received_at, user_id, role, clinic_id, module, action,
    object_type, object_id, result, details, correlation_id, producer, source_message_id, prev_hash, hash
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18
);

-- name: ListRecords :many
SELECT audit_record_id, seq, occurred_at, received_at, user_id, role, clinic_id, module, action,
       object_type, object_id, result, details, correlation_id, producer, source_message_id, prev_hash, hash
FROM audit_record
WHERE (sqlc.narg('module')::text IS NULL OR module = sqlc.narg('module'))
  AND (sqlc.narg('action')::text IS NULL OR action = sqlc.narg('action'))
  AND (sqlc.narg('object_type')::text IS NULL OR object_type = sqlc.narg('object_type'))
  AND (sqlc.narg('object_id')::text IS NULL OR object_id = sqlc.narg('object_id'))
  AND (sqlc.narg('user_id')::text IS NULL OR user_id = sqlc.narg('user_id'))
  AND (sqlc.narg('occurred_from')::timestamptz IS NULL OR occurred_at >= sqlc.narg('occurred_from'))
  AND (sqlc.narg('occurred_to')::timestamptz IS NULL OR occurred_at < sqlc.narg('occurred_to'))
  AND seq > sqlc.arg('after_seq')
ORDER BY seq
LIMIT sqlc.arg('page_limit');

-- name: RecordsFromSeq :many
SELECT audit_record_id, seq, occurred_at, received_at, user_id, role, clinic_id, module, action,
       object_type, object_id, result, details, correlation_id, producer, source_message_id, prev_hash, hash
FROM audit_record
WHERE seq > $1
ORDER BY seq
LIMIT $2;

-- name: CountRecords :one
SELECT count(*) FROM audit_record;
