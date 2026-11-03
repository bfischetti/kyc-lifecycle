-- name: CreateEntity :one
INSERT INTO entities (type, legal_name, country)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetEntity :one
SELECT * FROM entities WHERE id = $1;
