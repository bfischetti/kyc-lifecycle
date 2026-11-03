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
