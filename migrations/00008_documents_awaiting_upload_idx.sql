-- +goose Up

-- The upload-expiry sweep looks only at intents still awaiting upload. A
-- partial index holds just those rows, so the sweep's cost follows the
-- number of open intents rather than every document ever stored.
CREATE INDEX documents_awaiting_upload_idx ON documents (upload_expires_at)
    WHERE upload_status = 'awaiting_upload';

-- +goose Down

DROP INDEX documents_awaiting_upload_idx;
