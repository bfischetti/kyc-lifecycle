-- name: InsertStatusHistory :exec
-- No RETURNING: under RLS, returning rows requires the SELECT policy, and
-- some writers (support agents) may append history but not read it.
INSERT INTO status_history (entity_id, from_status, to_status, entity_version, reason, changed_by)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: TransactionTime :one
-- now() is fixed for the whole transaction, so this equals the changed_at
-- and uploaded_at written in it.
SELECT now()::timestamptz AS now;

-- name: ListStatusHistory :many
SELECT * FROM status_history WHERE entity_id = $1 ORDER BY entity_version;

-- name: InsertOutbox :exec
INSERT INTO outbox (id, entity_id, event_type, payload)
VALUES ($1, $2, $3, $4);

-- name: CurrentCycleStart :one
-- A Review Cycle starts when the Entity last entered pending_review.
SELECT max(changed_at)::timestamptz AS started_at
FROM status_history
WHERE entity_id = $1 AND to_status = 'pending_review';

-- name: ClaimNextOutbox :one
-- Locks one due notification. SKIP LOCKED lets several workers run side by
-- side: each claims a different row, and none waits on another's.
SELECT * FROM outbox
WHERE published_at IS NULL AND dead_at IS NULL AND next_attempt_at <= now()
ORDER BY created_at, id
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- name: MarkOutboxPublished :exec
UPDATE outbox SET published_at = now(), attempts = attempts + 1, last_error = NULL
WHERE id = $1;

-- name: MarkOutboxFailed :exec
UPDATE outbox
SET attempts        = attempts + 1,
    last_error      = sqlc.arg(last_error),
    next_attempt_at = sqlc.arg(next_attempt_at),
    dead_at         = CASE WHEN sqlc.arg(dead)::boolean THEN now() END
WHERE id = sqlc.arg(id);
