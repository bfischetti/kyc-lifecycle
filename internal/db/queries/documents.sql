-- name: CreateDocument :one
INSERT INTO documents (entity_id, doc_type, expiry_date, content_type, size_bytes, sha256, vault_key, upload_expires_at, declared_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetDocument :one
SELECT * FROM documents WHERE id = $1 AND entity_id = $2;

-- name: GetDocumentForUpdate :one
SELECT * FROM documents WHERE id = $1 AND entity_id = $2 FOR UPDATE;

-- name: MarkDocumentUploaded :one
-- Matches nothing once the upload permission has lapsed: an Upload Intent
-- completes only within its window, even if the bytes arrived in time.
UPDATE documents
SET upload_status = 'uploaded', uploaded_at = now(), completed_by = $2
WHERE id = $1 AND upload_status = 'awaiting_upload' AND upload_expires_at > now()
RETURNING *;

-- name: ExpireLapsedUploads :execrows
-- Turns Upload Intents whose permission has lapsed into Expired Uploads.
UPDATE documents
SET upload_status = 'expired'
WHERE upload_status = 'awaiting_upload' AND upload_expires_at <= now();

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
