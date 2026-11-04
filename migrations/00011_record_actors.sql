-- +goose Up

-- Who created an Entity, and who declared and completed a Document. Rows from
-- before this migration keep NULL: their actors were never recorded, and the
-- Audit Trail says so rather than guessing.
ALTER TABLE entities ADD COLUMN created_by uuid REFERENCES users (id);
ALTER TABLE documents
    ADD COLUMN declared_by  uuid REFERENCES users (id),
    ADD COLUMN completed_by uuid REFERENCES users (id);

-- +goose Down

ALTER TABLE documents
    DROP COLUMN completed_by,
    DROP COLUMN declared_by;
ALTER TABLE entities DROP COLUMN created_by;
