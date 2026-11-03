-- +goose Up

CREATE TYPE check_type   AS ENUM ('sanctions_screening', 'pep_screening', 'address_verification');
CREATE TYPE check_result AS ENUM ('pass', 'fail', 'manual_review');

-- One row per screening answer. Immutable: re-running a check adds a row.
CREATE TABLE checks (
    id         uuid         PRIMARY KEY DEFAULT gen_random_uuid(),
    entity_id  uuid         NOT NULL REFERENCES entities (id),
    check_type check_type   NOT NULL,
    provider   text         NOT NULL,
    result     check_result NOT NULL,
    run_by     uuid         NOT NULL REFERENCES users (id),
    created_at timestamptz  NOT NULL DEFAULT now()
);

CREATE INDEX checks_entity_id_idx ON checks (entity_id, check_type, created_at);

-- The provider's raw answer lives in its own table so the database can deny
-- it to support agents with row-level security (ADR-0001, #8).
CREATE TABLE check_payloads (
    check_id     uuid  PRIMARY KEY REFERENCES checks (id),
    raw_response jsonb NOT NULL
);

GRANT SELECT, INSERT ON checks         TO kyc_app;
GRANT SELECT, INSERT ON check_payloads TO kyc_app;

-- +goose Down

DROP TABLE check_payloads;
DROP TABLE checks;
DROP TYPE check_result;
DROP TYPE check_type;
