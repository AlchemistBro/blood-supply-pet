-- Заказы перевозки. Условие «строке есть что делать с C03» повторяет частичный индекс transport_request_due_idx.

-- name: InsertRequest :execrows
-- Повтор команды или отмена, обогнавшая заказ, упираются в первичный ключ: 0 строк, второго заказа нет.
INSERT INTO transport_request (
    obligation_id, request_id, source_clinic_id, target_clinic_id, component_kind_code, quantity,
    temperature_regime, status, next_attempt_at, created_at, updated_at
) VALUES (
    @obligation_id, @request_id, @source_clinic_id, @target_clinic_id, @component_kind_code, @quantity,
    @temperature_regime, 'REQUESTING', @now, @now, @now
)
ON CONFLICT (obligation_id) DO NOTHING;

-- name: LockStatus :one
SELECT status FROM transport_request WHERE obligation_id = @obligation_id FOR UPDATE;

-- name: InsertTombstone :exec
-- Надгробие: отмена пришла раньше заказа. Поздний RequestTransport упрётся в первичный ключ.
INSERT INTO transport_request (obligation_id, status, cancel_requested, next_attempt_at, created_at, updated_at)
VALUES (@obligation_id, 'CANCELLED', true, @now, @now, @now);

-- name: FlagCancel :exec
-- Отмена существующего заказа: флаг и немедленная очередь сверщику; снимает заказ у C03 drive.
UPDATE transport_request
SET cancel_requested = true, next_attempt_at = @now, updated_at = @now
WHERE obligation_id = @obligation_id;

-- name: Claim :one
-- Аренда: строку берёт тот, кто первым увидел её свободной. Конкурент после ожидания блокировки строки
-- перечитывает условие и получает 0 строк (READ COMMITTED), поэтому с C03 говорит один экземпляр.
UPDATE transport_request
SET lease_owner = @owner::uuid, lease_until = @lease_until::timestamptz
WHERE obligation_id = @obligation_id
  AND (lease_until IS NULL OR lease_until < @now::timestamptz)
  AND (status IN ('REQUESTING', 'UNKNOWN') OR (status = 'ACCEPTED' AND cancel_requested))
RETURNING *;

-- name: Finish :execrows
-- Исход записывается, только если аренда всё ещё наша (CAS по lease_owner).
UPDATE transport_request
SET status = @status, shipment_ref = coalesce(sqlc.narg('shipment_ref'), shipment_ref),
    lease_owner = NULL, lease_until = NULL, last_error = NULL, updated_at = @now
WHERE obligation_id = @obligation_id AND lease_owner = @owner::uuid;

-- name: FailAttempt :execrows
-- C03 недоступна: попытка засчитана, следующая не раньше next_attempt_at. Тоже только под нашей арендой.
UPDATE transport_request
SET status = @status, attempts = attempts + 1, last_error = @last_error, next_attempt_at = @next_attempt_at,
    lease_owner = NULL, lease_until = NULL, updated_at = @now
WHERE obligation_id = @obligation_id AND lease_owner = @owner::uuid;

-- name: Due :many
-- Очередь сверщика: просрочившие попытку, включая строки упавших экземпляров с истёкшей арендой.
SELECT obligation_id FROM transport_request
WHERE (status IN ('REQUESTING', 'UNKNOWN') OR (status = 'ACCEPTED' AND cancel_requested))
  AND next_attempt_at <= @now
  AND (lease_until IS NULL OR lease_until < @now::timestamptz)
ORDER BY next_attempt_at
LIMIT @batch_limit;

-- name: Find :one
SELECT * FROM transport_request WHERE obligation_id = @obligation_id;
