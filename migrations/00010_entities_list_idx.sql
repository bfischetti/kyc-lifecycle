-- +goose Up

-- The Entity list pages by (updated_at, id), with or without a status filter.
CREATE INDEX entities_status_updated_idx ON entities (status, updated_at, id);
CREATE INDEX entities_updated_idx ON entities (updated_at, id);

-- +goose Down

DROP INDEX entities_updated_idx;
DROP INDEX entities_status_updated_idx;
