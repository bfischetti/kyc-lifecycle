-- name: RecordEvidenceAccess :exec
-- No RETURNING, so the insert needs only the INSERT policy.
INSERT INTO evidence_access (document_id, entity_id, accessed_by)
VALUES ($1, $2, $3);
