# KYC/KYB Lifecycle Service

A Go service that takes a merchant or individual from first contact through document collection, screening and a compliance decision, with an audit record of every status change.

> 🚧 Under construction: work is tracked as [issues](https://github.com/bfischetti/kyc-lifecycle/issues), one PR per issue. A full walkthrough and the design rationale arrive in #15.

- **Domain language:** [`CONTEXT.md`](CONTEXT.md)
- **Architecture decisions:** [`docs/adr/`](docs/adr/)

## Getting started

Requirements: Go (version in `go.mod`), Docker.

```sh
cp .env.example .env    # dev-only defaults; change POSTGRES_PORT if 5432 is taken
make up                 # start Postgres and the Evidence Vault (RustFS)
make migrate            # apply migrations as the owner role
make serve              # run the API on :8080
```

Create and read an Entity as the seeded support agent:

```sh
curl -s -X POST localhost:8080/entities \
  -H 'X-User-ID: 00000000-0000-0000-0000-000000000002' \
  -d '{"type":"business","legal_name":"Acme Ltd","country":"GB"}'

curl -s localhost:8080/entities/<id> \
  -H 'X-User-ID: 00000000-0000-0000-0000-000000000002'
```

### Seeded users

There is no real authentication; `X-User-ID` stands in for a verified identity. The role always comes from the database.

| User ID | Role |
|---|---|
| `00000000-0000-0000-0000-000000000001` | compliance_officer |
| `00000000-0000-0000-0000-000000000002` | support_agent |
| `00000000-0000-0000-0000-000000000003` | system |

### Database roles

| Role | Used by | Notes |
|---|---|---|
| `kyc_owner` | `server migrate` | Owns the database and every table. |
| `kyc_app` | `server serve` | Runtime login: not an owner, not a superuser, cannot bypass row-level security ([ADR-0001](docs/adr/0001-evidence-vault-with-rls-backstop.md)). |

Both are created by [`deploy/postgres/init-roles.sh`](deploy/postgres/init-roles.sh) on first start.

## Development

```sh
make test               # unit tests
make test-integration   # + integration tests against real Postgres and RustFS (needs Docker)
make lint
make sqlc               # regenerate query code after editing migrations/ or internal/db/queries/
```
