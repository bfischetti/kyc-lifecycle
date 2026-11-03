-- +goose Up

-- Audit trail: one row per Verification Status change, written in the same
-- transaction as the change. Append-only for the runtime role.
CREATE TABLE status_history (
    id             uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
    entity_id      uuid          NOT NULL REFERENCES entities (id),
    from_status    entity_status NOT NULL,
    to_status      entity_status NOT NULL CHECK (to_status <> from_status),
    -- The Entity Version this change produced.
    entity_version integer       NOT NULL CHECK (entity_version > 0),
    reason         text          NOT NULL CHECK (btrim(reason) <> ''),
    changed_by     uuid          NOT NULL REFERENCES users (id),
    changed_at     timestamptz   NOT NULL DEFAULT now(),
    UNIQUE (entity_id, entity_version)
);

-- Transactional outbox: pending Status Change Notifications, written in the
-- same transaction as the change and delivered later by a worker (#10).
CREATE TABLE outbox (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(), -- the notification's event_id
    entity_id       uuid        NOT NULL REFERENCES entities (id),
    event_type      text        NOT NULL,
    payload         jsonb       NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    attempts        integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error      text,
    published_at    timestamptz,
    dead_at         timestamptz
);

CREATE INDEX outbox_pending_idx ON outbox (next_attempt_at)
    WHERE published_at IS NULL AND dead_at IS NULL;

GRANT SELECT, INSERT         ON status_history TO kyc_app;
GRANT SELECT, INSERT, UPDATE ON outbox         TO kyc_app;

-- +goose Down

DROP TABLE outbox;
DROP TABLE status_history;
