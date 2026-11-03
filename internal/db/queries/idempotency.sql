-- name: ClaimIdempotencyKey :execrows
-- Claims a new key; affects no row if the key is already known.
INSERT INTO idempotency_keys (user_id, key, fingerprint)
VALUES ($1, $2, $3)
ON CONFLICT (user_id, key) DO NOTHING;

-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys
WHERE user_id = $1 AND key = $2;

-- name: TakeOverIdempotencyKey :execrows
-- Re-claims a key whose holder never finished (e.g. the process died),
-- once its lease has run out.
UPDATE idempotency_keys
SET started_at = now()
WHERE user_id = $1 AND key = $2 AND completed_at IS NULL
  AND started_at < now() - make_interval(secs => sqlc.arg(lease_seconds)::float8);

-- name: CompleteIdempotencyKey :exec
UPDATE idempotency_keys
SET response_status = $3, response_body = $4, completed_at = now()
WHERE user_id = $1 AND key = $2 AND completed_at IS NULL;

-- name: ReleaseIdempotencyKey :exec
-- Forgets an unfinished claim, so the same key can be retried.
DELETE FROM idempotency_keys
WHERE user_id = $1 AND key = $2 AND completed_at IS NULL;
