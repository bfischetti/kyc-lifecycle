-- +goose Up

-- Evidence Access log: one row per opening of a Document's contents.
-- Append-only for the application role (no UPDATE or DELETE grant), so the
-- record of who looked at what cannot be edited after the fact.
CREATE TABLE evidence_access (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id uuid        NOT NULL REFERENCES documents (id),
    entity_id   uuid        NOT NULL REFERENCES entities (id),
    accessed_by uuid        NOT NULL REFERENCES users (id),
    accessed_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX evidence_access_document_idx ON evidence_access (document_id, accessed_at);

GRANT SELECT, INSERT ON evidence_access TO kyc_app;

-- RLS backstop (ADR-0001): only compliance officers access evidence, so only
-- they may record an access, only in their own name, and only they may read
-- the log.
ALTER TABLE evidence_access ENABLE ROW LEVEL SECURITY;
ALTER TABLE evidence_access FORCE ROW LEVEL SECURITY;

CREATE POLICY evidence_access_read ON evidence_access FOR SELECT
    USING (current_setting('app.user_role', true) = 'compliance_officer');
CREATE POLICY evidence_access_record ON evidence_access FOR INSERT
    WITH CHECK (
        current_setting('app.user_role', true) = 'compliance_officer'
        AND accessed_by = nullif(current_setting('app.user_id', true), '')::uuid
    );

-- +goose Down

DROP TABLE evidence_access;
