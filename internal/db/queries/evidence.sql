-- name: RecordEvidenceAccess :exec
-- No RETURNING, so the insert needs only the INSERT policy.
INSERT INTO evidence_access (document_id, entity_id, accessed_by)
VALUES ($1, $2, $3);

-- name: ListEvidenceAccessForEntity :many
-- Row-level security lets only compliance officers see these rows.
SELECT a.accessed_by, a.accessed_at, d.doc_type
FROM evidence_access a
JOIN documents d ON d.id = a.document_id
WHERE a.entity_id = $1
ORDER BY a.accessed_at, a.id;
