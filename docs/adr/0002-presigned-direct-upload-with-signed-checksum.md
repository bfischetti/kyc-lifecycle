# Presigned direct upload with a signed checksum

Clients upload document bytes directly to the Evidence Vault with a presigned PUT, not through the API. To keep the server in control of what it never sees in transit, the client declares content type, size and sha256 up front and those values are signed into the URL (`Content-Type`, `Content-Length`, `x-amz-checksum-sha256`), so storage rejects any mismatch. An explicit `/complete` call then verifies the object (HeadObject + checksum, magic-byte sniff of the first 512 bytes) before the single DB transaction that marks the document complete and drives any status transition. Status changes happen only on completion, never on intent.

## Considered Options

- **Proxy upload through the API.** Simpler, and the server sees the bytes, but it routes all upload traffic through the service and is not how high-volume or mobile-SDK flows work.
- **Presign the key only and trust what arrives.** Common but weak: a client can substitute any file.
- **S3 event notifications instead of `/complete`.** Realistic, but needs notification config plus a queue for the same guarantee; left as a production note.

## Consequences

- Documents carry an upload status (`awaiting_upload | uploaded | expired`) separate from review status.
- Abandoned intents must be expired by a sweeper; orphaned objects are harmless (private, random key) and left to a bucket lifecycle rule.
- Depends on MinIO honouring signed `x-amz-checksum-sha256`; spike first, and fall back to streaming the object to verify on `/complete`.
