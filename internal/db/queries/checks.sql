-- name: InsertCheck :one
INSERT INTO checks (entity_id, check_type, provider, result, run_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: InsertCheckPayload :exec
INSERT INTO check_payloads (check_id, raw_response) VALUES ($1, $2);
