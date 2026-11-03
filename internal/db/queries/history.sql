-- name: InsertStatusHistory :one
INSERT INTO status_history (entity_id, from_status, to_status, entity_version, reason, changed_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListStatusHistory :many
SELECT * FROM status_history WHERE entity_id = $1 ORDER BY entity_version;

-- name: InsertOutbox :exec
INSERT INTO outbox (id, entity_id, event_type, payload)
VALUES ($1, $2, $3, $4);
