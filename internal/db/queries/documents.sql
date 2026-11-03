-- name: CreateDocument :one
INSERT INTO documents (entity_id, doc_type, expiry_date, content_type, size_bytes, sha256, vault_key, upload_expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;
