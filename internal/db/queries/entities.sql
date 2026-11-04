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

-- name: ListEntitiesOldestFirst :many
SELECT * FROM entities
WHERE (sqlc.narg(status)::entity_status IS NULL OR status = sqlc.narg(status))
  AND (sqlc.narg(type)::entity_type IS NULL OR type = sqlc.narg(type))
  AND (sqlc.narg(after_updated_at)::timestamptz IS NULL
       OR (updated_at, id) > (sqlc.narg(after_updated_at), sqlc.narg(after_id)::uuid))
ORDER BY updated_at, id
LIMIT $1;

-- name: ListEntitiesNewestFirst :many
SELECT * FROM entities
WHERE (sqlc.narg(status)::entity_status IS NULL OR status = sqlc.narg(status))
  AND (sqlc.narg(type)::entity_type IS NULL OR type = sqlc.narg(type))
  AND (sqlc.narg(after_updated_at)::timestamptz IS NULL
       OR (updated_at, id) < (sqlc.narg(after_updated_at), sqlc.narg(after_id)::uuid))
ORDER BY updated_at DESC, id DESC
LIMIT $1;
