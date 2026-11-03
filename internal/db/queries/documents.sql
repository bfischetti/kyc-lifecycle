-- name: CreateDocument :one
INSERT INTO documents (entity_id, doc_type, expiry_date, content_type, size_bytes, sha256, vault_key, upload_expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetDocument :one
SELECT * FROM documents WHERE id = $1 AND entity_id = $2;

-- name: GetDocumentForUpdate :one
SELECT * FROM documents WHERE id = $1 AND entity_id = $2 FOR UPDATE;

-- name: MarkDocumentUploaded :one
UPDATE documents
SET upload_status = 'uploaded', uploaded_at = now()
WHERE id = $1 AND upload_status = 'awaiting_upload'
RETURNING *;

-- name: ListDocumentsForEntity :many
SELECT * FROM documents WHERE entity_id = $1 ORDER BY created_at, id;

-- name: ListCompletedDocumentsSince :many
SELECT * FROM documents
WHERE entity_id = $1 AND upload_status = 'uploaded' AND uploaded_at >= sqlc.arg(since)
ORDER BY uploaded_at, id;

-- name: SettleCycleDocuments :exec
-- Applies the Review Cycle's outcome to its Completed Uploads still pending review.
UPDATE documents
SET review_status = sqlc.arg(review_status)
WHERE entity_id = sqlc.arg(entity_id)
  AND upload_status = 'uploaded'
  AND review_status = 'pending'
  AND uploaded_at >= sqlc.arg(since);
