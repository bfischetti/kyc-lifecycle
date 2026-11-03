-- +goose Up

CREATE TYPE document_type AS ENUM ('passport', 'national_id', 'business_registration', 'proof_of_address');

-- Whether the bytes exist in the Evidence Vault (ADR-0002). Separate from
-- review_status: "are the bytes there?" vs "did compliance accept them?".
CREATE TYPE upload_status AS ENUM ('awaiting_upload', 'uploaded', 'expired');

CREATE TYPE document_review_status AS ENUM ('pending', 'accepted', 'rejected');

CREATE TABLE documents (
    id                uuid                   PRIMARY KEY DEFAULT gen_random_uuid(),
    entity_id         uuid                   NOT NULL REFERENCES entities (id),
    doc_type          document_type          NOT NULL,
    expiry_date       date,
    -- Declared by the caller and signed into the upload URL.
    content_type      text                   NOT NULL,
    size_bytes        bigint                 NOT NULL CHECK (size_bytes > 0),
    sha256            text                   NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    -- Opaque Evidence Vault object key. Random, PII-free, never returned by the API.
    vault_key         text                   NOT NULL UNIQUE,
    upload_status     upload_status          NOT NULL DEFAULT 'awaiting_upload',
    upload_expires_at timestamptz            NOT NULL,
    uploaded_at       timestamptz,
    review_status     document_review_status NOT NULL DEFAULT 'pending',
    created_at        timestamptz            NOT NULL DEFAULT now()
);

CREATE INDEX documents_entity_id_idx ON documents (entity_id);

GRANT SELECT, INSERT, UPDATE ON documents TO kyc_app;

-- +goose Down

DROP TABLE documents;
DROP TYPE document_review_status;
DROP TYPE upload_status;
DROP TYPE document_type;
