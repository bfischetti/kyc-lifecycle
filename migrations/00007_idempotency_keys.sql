-- +goose Up

-- Idempotency keys for the endpoints where a duplicate is harmful: creating
-- an Entity (duplicate records) and running a Check (a second paid
-- provider call). A key belongs to one user; the fingerprint is a hash of
-- the request it was first used with.
--
-- A row is claimed (started_at, no response) before the work runs and
-- completed with the response afterwards, so a concurrent duplicate sees
-- the claim instead of doing the work again.
CREATE TABLE idempotency_keys (
    user_id         uuid        NOT NULL REFERENCES users (id),
    key             text        NOT NULL CHECK (length(key) BETWEEN 1 AND 255),
    fingerprint     bytea       NOT NULL,
    started_at      timestamptz NOT NULL DEFAULT now(),
    response_status integer,
    response_body   bytea,
    completed_at    timestamptz,
    PRIMARY KEY (user_id, key),
    CHECK ((completed_at IS NULL) = (response_status IS NULL))
);

GRANT SELECT, INSERT, UPDATE, DELETE ON idempotency_keys TO kyc_app;

-- RLS backstop: a caller only ever sees and writes their own keys.
ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys FORCE ROW LEVEL SECURITY;

CREATE POLICY idempotency_keys_own ON idempotency_keys FOR ALL
    USING (user_id = nullif(current_setting('app.user_id', true), '')::uuid)
    WITH CHECK (user_id = nullif(current_setting('app.user_id', true), '')::uuid);

-- +goose Down

DROP TABLE idempotency_keys;
