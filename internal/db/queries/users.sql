-- name: GetUserRole :one
SELECT role FROM users WHERE id = $1;
