# Presigned direct upload with a signed checksum

Clients upload document bytes directly to the Evidence Vault with a presigned PUT, not through the API. To keep the server in control of what it never sees in transit, the client declares content type, size and sha256 up front and those values are signed into the URL (`Content-Type`, `Content-Length`, `x-amz-checksum-sha256`), so storage rejects any mismatch. An explicit `/complete` call then verifies the object (HeadObject + checksum, magic-byte sniff of the first 512 bytes) before the single DB transaction that marks the document complete and drives any status transition. Status changes happen only on completion, never on intent.

## Considered Options

- **Proxy upload through the API.** Simpler, and the server sees the bytes, but it routes all upload traffic through the service and is not how high-volume or mobile-SDK flows work.
- **Presign the key only and trust what arrives.** Common but weak: a client can substitute any file.
- **S3 event notifications instead of `/complete`.** Realistic, but needs notification config plus a queue for the same guarantee; left as a production note.

## Consequences

- Documents carry an upload status (`awaiting_upload | uploaded | expired`) separate from review status.
- Abandoned intents must be expired by a sweeper; orphaned objects are harmless (private, random key) and left to a bucket lifecycle rule.
- Depends on the store rejecting uploads that do not match the signed constraints. Verified; see below.

## Store verification (spike, issue #2)

The design was written assuming MinIO, but MinIO stopped publishing community images in 2025, so the local Evidence Vault is **RustFS** (`rustfs/rustfs:1.0.4`, Apache-2.0, designed as a MinIO-compatible S3 server). SeaweedFS was tested too and behaved identically; RustFS was picked for its simpler setup (credentials via two env variables). Production would use S3 itself; the code depends only on the S3 API.

Presigning with `aws-sdk-go-v2` signs `Content-Type`, `Content-Length` and `x-amz-checksum-sha256` into the URL. Against RustFS 1.0.4:

| Upload | Result |
|---|---|
| Declared bytes, type and checksum | 200, stored, checksum matches |
| Different bytes, same size, declared checksum header | 400 `BadDigest`, nothing stored |
| Different bytes with their own checksum header | 403 `SignatureDoesNotMatch`, nothing stored |
| Body longer than declared | 403, nothing stored |
| Different content type | 403, nothing stored |
| Checksum header omitted | 403, nothing stored |

So the store enforces the declaration on its own, and the fallback (stream the object on `/complete` to re-hash it) is not needed. `/complete` still checks existence, size and checksum via `HeadObject`, plus magic bytes, as defence in depth. The store contract test in `internal/vault` asserts this table in CI, so upgrading or swapping the store cannot silently break it.

