-- name: CreateEntity :one
INSERT INTO entities (type, legal_name, country)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetEntity :one
SELECT * FROM entities WHERE id = $1;

-- name: GetEntityForUpdate :one
SELECT * FROM entities WHERE id = $1 FOR UPDATE;

-- name: UpdateEntityStatus :one
-- Guarded by the expected version so a lost update is impossible even if a
-- caller forgot to lock.
UPDATE entities
SET status = $3, version = version + 1, updated_at = now()
WHERE id = $1 AND version = $2
RETURNING *;
