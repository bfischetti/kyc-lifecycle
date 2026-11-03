-- +goose Up

CREATE TYPE user_role AS ENUM ('compliance_officer', 'support_agent', 'system');

CREATE TABLE users (
    id         uuid        PRIMARY KEY,
    role       user_role   NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Fixed, documented IDs so local curl examples and tests are reproducible.
-- The system user is a real row so every audit record points at a user.
INSERT INTO users (id, role) VALUES
    ('00000000-0000-0000-0000-000000000001', 'compliance_officer'),
    ('00000000-0000-0000-0000-000000000002', 'support_agent'),
    ('00000000-0000-0000-0000-000000000003', 'system');

CREATE TYPE entity_type   AS ENUM ('individual', 'business');
CREATE TYPE entity_status AS ENUM ('unverified', 'pending_review', 'verified', 'rejected');

CREATE TABLE entities (
    id         uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
    type       entity_type   NOT NULL,
    legal_name text          NOT NULL CHECK (btrim(legal_name) <> ''),
    country    char(2)       NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    status     entity_status NOT NULL DEFAULT 'unverified',
    -- Incremented on every status change; see "Entity Version" in CONTEXT.md.
    version    integer       NOT NULL DEFAULT 0 CHECK (version >= 0),
    created_at timestamptz   NOT NULL DEFAULT now(),
    updated_at timestamptz   NOT NULL DEFAULT now()
);

-- Least privilege for the runtime role: no DELETE anywhere, no writes to users.
GRANT SELECT                 ON users    TO kyc_app;
GRANT SELECT, INSERT, UPDATE ON entities TO kyc_app;

-- +goose Down

DROP TABLE entities;
DROP TYPE entity_status;
DROP TYPE entity_type;
DROP TABLE users;
DROP TYPE user_role;
