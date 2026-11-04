# KYC/KYB Lifecycle Service

A Go service that takes a merchant (KYB) or individual (KYC) from first contact through document collection, screening and a compliance decision. Every status change is audited and announced to downstream systems.

It is built to resemble how a regulated company would run this, not just to pass a spec. Document bytes live in a private evidence vault and never pass through the API. Every access to them is logged. Access is enforced in the application, and Postgres row-level security backs it up. Notifications go through a transactional outbox, so a status change and its notification are committed together.

- **Domain language:** [`CONTEXT.md`](CONTEXT.md). Words in **bold** below are defined there.
- **Architecture decisions:** [ADR-0001 evidence vault with RLS backstop](docs/adr/0001-evidence-vault-with-rls-backstop.md), [ADR-0002 presigned direct upload with signed checksum](docs/adr/0002-presigned-direct-upload-with-signed-checksum.md)

```
                 completed upload               approve (all requirements met)
  unverified ─────────────────────▶ pending_review ─────────────────────▶ verified (terminal)
                                     │      ▲
          reject (officer) or        │      │ completed upload
          hard fail (system)         ▼      │ (Resubmission: a new Review Cycle)
                                     rejected
```

The whole state machine is one table in [`internal/kyc/statemachine.go`](internal/kyc/statemachine.go), tested exhaustively for every status × event pair.

## Contents

- [Run it](#run-it)
- [Walkthrough: create → upload → check → approve](#walkthrough-create--upload--check--approve)
- [Design](#design)
- [Deliberate simplifications](#deliberate-simplifications)
- [What production would do differently](#what-production-would-do-differently)
- [Project layout](#project-layout)
- [Development](#development)

## Run it

Requirements: Go (version in `go.mod`), Docker, and `curl`, `jq` and `openssl` for the walkthrough.

```sh
cp .env.example .env   # dev-only defaults
make up                # Postgres, the Evidence Vault (RustFS) and the webhook-sink
make migrate           # apply migrations as the owner role
make serve             # the API on :8080, plus the outbox worker and upload-expiry sweeper
```

If ports 5432, 9000 or 8090 are taken, change `POSTGRES_PORT`, `VAULT_PORT` or `WEBHOOK_SINK_PORT` in `.env`. Also update the URLs that use them (`DATABASE_URL`, `MIGRATE_DATABASE_URL`, `VAULT_ENDPOINT`, `WEBHOOK_URL`).

In a second terminal, watch the notifications arrive:

```sh
make sink-logs
```

`make down` stops everything and deletes its data.

### Users and roles

There is no real authentication. `X-User-ID` stands in for a verified identity, and the role always comes from the database, never the request.

| User ID | Role | May |
|---|---|---|
| `00000000-0000-0000-0000-000000000001` | compliance_officer | everything: screen, decide, read the audit trail, open evidence |
| `00000000-0000-0000-0000-000000000002` | support_agent | onboard Entities and attach Documents; sees the **Redacted View** |
| `00000000-0000-0000-0000-000000000003` | system | automated work: checks, System Rejections, notification delivery |

The full matrix is one map in [`internal/policy/policy.go`](internal/policy/policy.go).

## Walkthrough: create → upload → check → approve

Run these in one shell, with `make serve` running.

```sh
cd "$(mktemp -d)"   # the walkthrough writes a couple of sample files
API=http://localhost:8080
AGENT='X-User-ID: 00000000-0000-0000-0000-000000000002'
OFFICER='X-User-ID: 00000000-0000-0000-0000-000000000001'
```

**1. A support agent onboards an individual.** It starts `unverified` at version 0.

```sh
ENTITY=$(curl -s -X POST $API/entities -H "$AGENT" \
  -d '{"type":"individual","legal_name":"Ada Lovelace","country":"GB"}' | jq -r .id)
echo $ENTITY
```

**2. Declare a passport.** This creates an **Upload Intent**. The response carries a short-lived upload permission with the declared size, type and SHA-256 signed into it. The Entity doesn't change yet.

```sh
printf '%%PDF-1.7\n%% sample passport scan\n' > passport.pdf
SHA=$(openssl sha256 -r passport.pdf | cut -d' ' -f1)
SIZE=$(wc -c < passport.pdf | tr -d ' ')

DECLARED=$(curl -s -X POST $API/entities/$ENTITY/documents -H "$AGENT" \
  -d "{\"doc_type\":\"passport\",\"content_type\":\"application/pdf\",\"size_bytes\":$SIZE,\"sha256\":\"$SHA\"}")
DOC=$(echo "$DECLARED" | jq -r .document.id)
echo "$DECLARED" | jq .upload.headers
```

**3. Upload straight to the vault, then complete.** The bytes go to the vault, not through the API. The store rejects anything that differs from the declaration. Completing re-checks the object (size, checksum, type, magic bytes) and opens a **Review Cycle**.

```sh
curl -s -o /dev/null -w 'vault: %{http_code}\n' -X PUT "$(echo "$DECLARED" | jq -r .upload.url)" \
  -H 'Content-Type: application/pdf' \
  -H "X-Amz-Checksum-Sha256: $(echo "$DECLARED" | jq -r '.upload.headers["X-Amz-Checksum-Sha256"]')" \
  --data-binary @passport.pdf

curl -s -X POST $API/entities/$ENTITY/documents/$DOC/complete -H "$AGENT" | jq -c .entity
# {"status":"pending_review","version":1,...}
```

The sink logs its first notification: `unverified → pending_review`.

**4. Screen.** An individual needs sanctions and PEP checks. The provider is a deterministic mock: the name "Ada Lovelace" passes everything.

```sh
for check in sanctions_screening pep_screening; do
  curl -s -X POST $API/entities/$ENTITY/checks -H "$OFFICER" -d "{\"check_type\":\"$check\"}" | jq -c '.check | {check_type, result}'
done
```

**5. Approve,** citing the version you reviewed. A decision made on an outdated version is refused as a **Stale Decision**.

```sh
curl -s -X POST $API/entities/$ENTITY/review -H "$OFFICER" \
  -d '{"decision":"approve","reason":"sanctions and PEP clear, passport valid","expected_version":1}' | jq -c '{status, version}'
# {"status":"verified","version":2}
```

The sink logs `pending_review → verified`, with `"signature_verified":true`.

**6. Read the audit trail** (compliance only):

```sh
curl -s $API/entities/$ENTITY/history -H "$OFFICER" | jq -c '.changes[] | {from_status, to_status, changed_by, reason}'
```

### Also worth trying

```sh
# The same Entity as each role sees it: payloads present for the officer, absent for the agent.
curl -s $API/entities/$ENTITY -H "$OFFICER" | jq '.checks[0] | has("raw_response")'   # true
curl -s $API/entities/$ENTITY -H "$AGENT"   | jq '.checks[0] | has("raw_response")'   # false

# Evidence Access: recorded, then a 302 to a 60-second download link. Agents get 403.
curl -s -o /dev/null -w '%{http_code}\n' $API/entities/$ENTITY/documents/$DOC/content -H "$AGENT"   # 403
curl -s -L -o downloaded.pdf $API/entities/$ENTITY/documents/$DOC/content -H "$OFFICER" && cmp passport.pdf downloaded.pdf && echo identical

# Approval refused: a new Entity with no screening yet lists every unmet requirement.
# A legal name containing SANCTIONED fails sanctions screening, which rejects the Entity automatically (System Rejection).

# Safe retries: the same Idempotency-Key returns the original response; no second Entity is created.
curl -s -X POST $API/entities -H "$AGENT" -H 'Idempotency-Key: demo-1' -d '{"type":"business","legal_name":"Acme Ltd","country":"GB"}' | jq -r .id
curl -s -X POST $API/entities -H "$AGENT" -H 'Idempotency-Key: demo-1' -d '{"type":"business","legal_name":"Acme Ltd","country":"GB"}' | jq -r .id

# Operational metrics (notifications are delivered asynchronously, so pending may briefly be non-zero).
curl -s $API/metrics | grep -E '^kyc_(checks_total\{.*\} [1-9]|status_transitions_total|notifications)'
```

Other magic names for the mock provider: `PEP` (manual review), `BADADDRESS` (address verification fails), `TIMEOUT` and `PROVIDERDOWN` (Provider Errors: 502, nothing recorded, safe to retry). See [`internal/screening/mock.go`](internal/screening/mock.go).

## Design

### The application enforces access; RLS is the backstop

([ADR-0001](docs/adr/0001-evidence-vault-with-rls-backstop.md))

Who may do what, and which fields each role sees, is decided in one place: [`internal/policy`](internal/policy/policy.go). Every use case asks it first, and handlers never compare roles. That module is the control.

Row-level security is a second, independent layer on the most sensitive tables: raw provider payloads, the audit trail, the evidence access log, and idempotency keys. Each transaction sets the caller's identity with `set_config(..., true)`, which is transaction-local, so it can't leak to the next request on a pooled connection. The app connects as `kyc_app`, which doesn't own the tables and can't bypass RLS, and the policies are `FORCE`d, so they bind the owner as well.

**What RLS protects against here:**
- A query bug in the app that over-selects. For example, a support agent's request accidentally reading payloads still gets no rows.
- Ad-hoc SQL as the application role.

These are proven with raw SQL as `kyc_app` in [`TestRLSBackstop`](internal/httpapi/redaction_integration_test.go), [`TestEvidenceAccessLogBackstop`](internal/httpapi/evidence_integration_test.go), [`TestIdempotencyKeysBackstop`](internal/httpapi/idempotency_integration_test.go) and [`TestRuntimeRoleCannotBypassRLS`](internal/httpapi/entities_integration_test.go). [`TestCallerRoleDoesNotLeakAcrossPooledConnections`](internal/httpapi/redaction_integration_test.go) shows the identity settings don't leak between requests on pooled connections.

**What it does not protect against:**
- **A compromised application.** The app sets the role variable itself, so code running as `kyc_app` can claim any role.
- **Database superusers.**
- **Column-level exposure.** RLS filters rows, not columns, which is why hiding fields (the **Redacted View**) is the policy module's job and why raw payloads live in their own table.

That's why it's a backstop and not the primary control. The spec suggested the reverse, and ADR-0001 records why that changed.

### Evidence lives in a vault; uploads go straight to it

([ADR-0002](docs/adr/0002-presigned-direct-upload-with-signed-checksum.md))

Document bytes are stored in S3-compatible object storage (RustFS locally) under random keys that reveal nothing about the Entity. The API never returns these keys and never logs them (asserted in [`TestUploadIntent`](internal/httpapi/documents_integration_test.go)).

**Uploads.** The client declares type, size and SHA-256, then receives a presigned PUT with all three signed in. The store itself rejects any other bytes. [`TestPresignedPutEnforcesSignedConstraints`](internal/vault/store_contract_integration_test.go) pins that down against the real store: a wrong body, size, type or checksum gets 400 or 403.

**Completion.** A Document only counts once the client calls `/complete`. The service then checks the object again independently: existence, size, checksum, content type and magic bytes ([`TestCompletedUpload`](internal/httpapi/complete_integration_test.go)). The status changes only on completion. An intent not completed in time becomes an **Expired Upload** and can never complete, even if its bytes arrived in time ([`TestUploadIntentExpiry`](internal/httpapi/expiry_integration_test.go)).

**Reading evidence.** The only way back to the bytes is an **Evidence Access**. A compliance officer's request is recorded in an append-only log first, then answered with a 302 to a 60-second download link. The file is served as an attachment named after the document id ([`TestEvidenceAccess`](internal/httpapi/evidence_integration_test.go), [`TestEvidenceURLExpires`](internal/httpapi/evidence_integration_test.go)).

### Status changes and notifications commit together (transactional outbox)

Every status change happens in one transaction that:
- locks the Entity row
- updates its status and **Entity Version**
- appends one audit record
- writes one outbox row

Either all of it happens or none does ([`internal/app/transition.go`](internal/app/transition.go)). A background worker then delivers outbox rows ([`internal/outbox`](internal/outbox/worker.go)):

- **Delivery is at least once.** A row stays locked while it's being published, so two workers never send the same notification concurrently ([`TestTwoWorkersNeverPublishTheSameNotificationAtOnce`](internal/outbox/worker_integration_test.go)). A crash between sending and committing means it's sent again, so consumers de-duplicate on `event_id`. The sink shows this by flagging redeliveries as duplicates.
- **Ordering is not guaranteed, and the design doesn't depend on it.** A failed delivery backs off, so a later notification for the same Entity can overtake it. Each notification carries the Entity Version, and consumers discard anything older than what they've already seen.
- **Failures retry with exponential backoff.** After 10 attempts a notification is dead-lettered, kept and visible rather than dropped ([`TestDelivery`](internal/outbox/worker_integration_test.go)). The `kyc_notifications{state="dead"}` gauge exposes the count.
- **Webhooks are signed** using the [Standard Webhooks](https://www.standardwebhooks.com) scheme: HMAC-SHA256 over id, timestamp and body. Receivers can reject forged, tampered or replayed deliveries ([`TestWebhookDelivery`](internal/outbox/webhook_integration_test.go), [`cmd/webhook-sink`](cmd/webhook-sink/main_test.go)).

Notifications contain identifiers and the transition only, never document contents, provider payloads or written decision reasons.

### Concurrency and retries

- **Lock order.** Transactions lock the Entity row first, then any Document rows, everywhere ([`internal/app/transition.go`](internal/app/transition.go)).
- **Stale decisions.** A decision must state the `expected_version` the officer reviewed. Two simultaneous approvals give one success and one 409 ([`TestReview`](internal/httpapi/review_integration_test.go)).
- **No lock held during the provider call.** The screening provider is called outside any transaction, under a timeout. If the Entity changed meanwhile, the answer is discarded ([`TestScreeningDiscardsAnswerIfEntityChangedMeanwhile`](internal/httpapi/checks_integration_test.go)). A **Provider Error** records nothing and returns a retryable 502.
- **Idempotency keys.** `POST /entities` and `POST /entities/{id}/checks` accept an `Idempotency-Key`. Concurrent duplicates do the work once, so there's no second Entity and no second paid provider call ([`TestIdempotentChecks`](internal/httpapi/idempotency_integration_test.go)). Keys are kept for 24 hours (`IDEMPOTENCY_KEY_RETENTION`), then deleted by the sweeper ([`TestIdempotencyKeyRetention`](internal/httpapi/idempotency_retention_integration_test.go)).

### Observability

- **Logs.** JSON logs via `slog`. Every line carries the request id, and the access log also carries the user, entity and document ids. Vault keys, URLs and secrets are never logged.
- **Metrics.** `/metrics` exposes checks by type and result, Provider Errors, transitions, the notification backlog, and HTTP latency by route pattern ([`TestMetrics`](internal/httpapi/metrics_integration_test.go)).

## Deliberate simplifications

- **No override and no four-eyes.** A **Compliance Approval** requires every **Required Check** to pass and the **Required Documents** to be present and unexpired. No one can approve past a failing or pending check, and no second officer has to confirm. Real programmes often allow an override with escalation and dual control.
- **Screening is synchronous.** The officer's request waits for the provider, which is bounded by `PROVIDER_TIMEOUT`. Real vendors are often asynchronous, with callbacks or polling.
- **No periodic re-KYC or ongoing monitoring.** `verified` is terminal. Real programmes re-screen on a schedule and on list updates, and can move a verified customer back into review.
- **No read model for support.** The **Redacted View** is computed per request from the same tables.
- **Identity is a header.** `X-User-ID` stands in for a verified identity from an identity provider.
- **The screening provider is a mock** driven by magic names, as vendor sandboxes are.

## What production would do differently

- **Keys and encryption.** Encrypt the vault with KMS-managed keys (per-tenant or per-document data keys), rotate the webhook secret with overlapping signatures (the verifier already accepts several), and keep secrets in a secret manager, not env files.
- **A dedicated vault service.** Put the vault behind its own service and account, so the API cannot read evidence at all, only ask for an audited download link.
- **S3 events.** Use object-created events to complete uploads automatically instead of relying on the client's `/complete` call, and quarantine and malware-scan uploads before they count.
- **A message broker.** Relay the outbox to a broker such as Kafka or SNS/SQS rather than to webhooks directly, with per-entity partitioning if consumers need ordering.
- **Ongoing monitoring.** Re-screen when sanctions and PEP lists change, add adverse-media screening, and make periodic reviews risk-based.
- **Retention and deletion.** Keep evidence, audit trails and access logs as long as regulation requires. After that, delete them (including vault objects and expired uploads). Idempotency keys already have a retention window here; the rest of the data would get one too.
- **Break-glass access.** Allow time-boxed, approved emergency access to evidence or data, recorded and alerted on, in place of standing superuser access.
- **Real authentication.** Use OIDC with roles from the identity provider and service-to-service authentication for the system actor.

## Project layout

```
cmd/server            the service: `migrate` and `serve`
cmd/webhook-sink      stand-in Downstream Consumer that verifies and logs notifications
internal/kyc          domain types, state machine, approval rules (no I/O)
internal/policy       who may do what, and which fields each role sees
internal/app          use cases: one actor-scoped transaction each
internal/httpapi      HTTP transport: routing, middleware, JSON mapping
internal/db           connection, actor-scoped transactions, migrations; queries/ → dbgen/ (sqlc)
internal/vault        Evidence Vault client (presigned upload/download, verification reads)
internal/screening    Screening Provider interface and the deterministic mock
internal/outbox       notification worker, log and webhook publishers
internal/webhook      Standard Webhooks signing and verification
internal/expiry       sweeper: expires abandoned Upload Intents, deletes old idempotency keys
internal/metrics      Prometheus collectors
migrations/           goose SQL migrations, embedded in the binary
```

## Development

```sh
make test               # unit tests
make test-integration   # + integration tests against real Postgres and RustFS (needs Docker)
make lint
make sqlc               # regenerate query code after editing migrations/ or internal/db/queries/
```

Integration tests start throwaway containers with testcontainers, so they need nothing from `make up`. CI runs lint, the sqlc drift check, both test suites and the sink image build on every PR.
