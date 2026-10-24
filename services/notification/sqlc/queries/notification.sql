-- name: InsertObligation :exec
-- Повтор ObligationOpened строку не перезаписывает.
INSERT INTO obligation_projection (
    obligation_id, request_id, origin, source_clinic_id, target_clinic_id, component_kind_code,
    requested_qty, urgency_code, deadline, opened_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (obligation_id) DO NOTHING;

-- name: GetObligation :one
SELECT obligation_id, request_id, origin, source_clinic_id, target_clinic_id, component_kind_code,
       requested_qty, urgency_code, deadline, opened_at
FROM obligation_projection
WHERE obligation_id = $1;

-- name: InsertNotification :exec
INSERT INTO notification (
    notification_id, kind, obligation_id, request_id, source_message_id, recipient_role, recipient_clinic_id,
    recipient_email, subject, source_clinic_id, target_clinic_id, component_kind_code, quantity, urgency_code,
    deadline, reason, status, attempts, next_attempt_at, last_error, created_at, sent_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22
);

-- name: ClaimDue :many
-- Берёт созревшие записи и сдвигает следующую попытку на срок аренды: параллельный отправщик их не возьмёт,
-- а при падении процесса после взятия запись вернётся в работу. Счётчик попыток растёт при взятии.
UPDATE notification n
SET attempts = n.attempts + 1,
    next_attempt_at = sqlc.arg('lease_until')
WHERE n.notification_id IN (
    SELECT d.notification_id
    FROM notification d
    WHERE d.status = 'PENDING' AND d.next_attempt_at <= sqlc.arg('due_at')
    ORDER BY d.next_attempt_at
    LIMIT sqlc.arg('batch')
    FOR UPDATE SKIP LOCKED
)
RETURNING n.notification_id, n.kind, n.obligation_id, n.request_id, n.source_message_id, n.recipient_role,
          n.recipient_clinic_id, n.recipient_email, n.subject, n.source_clinic_id, n.target_clinic_id,
          n.component_kind_code, n.quantity, n.urgency_code, n.deadline, n.reason, n.status, n.attempts,
          n.next_attempt_at, n.last_error, n.created_at, n.sent_at;

-- name: MarkSent :execrows
UPDATE notification
SET status = 'SENT', sent_at = sqlc.arg('sent_at'), last_error = NULL
WHERE notification_id = sqlc.arg('notification_id') AND status = 'PENDING';

-- name: MarkRetry :execrows
UPDATE notification
SET next_attempt_at = sqlc.arg('next_attempt_at'), last_error = sqlc.arg('last_error')
WHERE notification_id = sqlc.arg('notification_id') AND status = 'PENDING';

-- name: MarkFailed :execrows
UPDATE notification
SET status = 'FAILED', last_error = sqlc.arg('last_error')
WHERE notification_id = sqlc.arg('notification_id') AND status = 'PENDING';

-- name: ListNotifications :many
SELECT notification_id, kind, obligation_id, request_id, source_message_id, recipient_role, recipient_clinic_id,
       recipient_email, subject, source_clinic_id, target_clinic_id, component_kind_code, quantity, urgency_code,
       deadline, reason, status, attempts, next_attempt_at, last_error, created_at, sent_at
FROM notification
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
ORDER BY created_at DESC, notification_id DESC
LIMIT sqlc.arg('page_limit');
