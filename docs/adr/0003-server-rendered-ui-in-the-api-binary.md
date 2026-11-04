# Server-rendered back-office UI in the API binary

The back-office UI for **Support Agents** and **Compliance Officers** is server-rendered with `html/template`, embedded in the existing Go binary and served on the API's origin under `/ui/`. Its handlers call `app.Service` directly rather than the JSON API, so `policy` and RLS apply exactly as they do for the API. There is no frontend framework or build step: styling is our own small stylesheet, and the only script is a small one that hashes files and uploads them to the Evidence Vault, because ADR-0002 keeps the bytes out of the service.

## Considered Options

- **SPA calling the JSON API.** Exercises the API contract, but adds a build tool, a CI job, API CORS and the evidence redirect problem, which is too much for a back-office tool.
- **`templ` instead of `html/template`.** Catches template mistakes at compile time, but adds a dependency and a generate step that a tool this small doesn't need.
- **Static HTML and JavaScript calling the JSON API.** Needs no build step, but means hand-written client rendering, and evidence links still can't send `X-User-ID`.
- **Uploading through the service, or a same-origin proxy to the vault.** Avoids bucket CORS, but the first reverses ADR-0002 and the second adds a component production wouldn't have. Instead, the bucket has a CORS rule for PUT only, limited to the UI's origin and the signed headers.
- **Server-side sessions or real authentication.** With no credentials behind it, a session table adds no security, and real authentication remains production work (see the README).

## Consequences

- Using the UI does not exercise the JSON handlers. Integration tests remain responsible for the API contract, and both front doors map service errors such as Stale Decisions and unmet approval requirements on their own.
- Identity is an unsigned "sign in as" cookie holding a seeded person's user id. It is exactly as trustworthy as the `X-User-ID` header: a demo stand-in, not authentication. The System user can't sign in.
- Cookies bring CSRF, which the header didn't have. The cookie is `SameSite=Strict` and every action is a POST, including opening evidence, so a GET can never add to the Audit Trail. Signing in needs no cookie, so the UI also refuses cross-origin POSTs outright.
- Evidence Access keeps ADR-0001's order: a POST records the access, then answers with a 303 redirect to the short-lived URL.
