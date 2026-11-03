# Evidence vault with RLS as a backstop

Document bytes live in private S3-compatible object storage (MinIO locally) under random, PII-free object keys; the database stores only the key and metadata, and the key is never returned by the API. Evidence is read only through a compliance-gated endpoint that writes an access-log row and redirects to a short-lived presigned URL. Field redaction and endpoint authorization are enforced in a single application policy module; Postgres RLS (keyed on a per-transaction `app.user_role` set with `SET LOCAL`, tables under `FORCE ROW LEVEL SECURITY`, app connecting as a non-owner role) is a second, independent layer on the most sensitive tables, not the primary control.

## Considered Options

- **RLS as the primary control** (the original spec). Rejected: RLS filters rows, not columns, so hiding fields forces either splitting tables purely for policy or column grants via `SET ROLE` — and the session variable is set by the app anyway, so it never protected against a compromised app.
- **Fake `storage_ref` path in the database.** Rejected: unrealistic. In production the pointer is not the secret; bucket access and presigned-URL issuance are, and that is where authorization and access logging belong.
- **App-layer only, no RLS.** Viable and common, but loses defense-in-depth against query bugs (an endpoint that over-selects, ad-hoc SQL access).

## Consequences

- Schema grows past the spec's five tables (access log, plus any payload split).
- Integration tests need MinIO alongside Postgres.
- Owner/app role separation is mandatory: table owners bypass RLS silently.
