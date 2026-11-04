-- name: InsertCheck :one
INSERT INTO checks (entity_id, check_type, provider, result, run_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: InsertCheckPayload :exec
INSERT INTO check_payloads (check_id, raw_response) VALUES ($1, $2);

-- name: ListChecksWithPayloads :many
-- The same query for every role. Row-level security decides whether the
-- payload row is visible; when it is not, raw_response comes back NULL.
SELECT c.*, p.raw_response
FROM checks c
LEFT JOIN check_payloads p ON p.check_id = c.id
WHERE c.entity_id = $1
ORDER BY c.created_at, c.id;

-- name: ListChecksSince :many
SELECT * FROM checks
WHERE entity_id = $1 AND created_at >= sqlc.arg(since)
ORDER BY created_at, id;

-- name: ListChecksForEntity :many
SELECT * FROM checks WHERE entity_id = $1 ORDER BY created_at, id;
