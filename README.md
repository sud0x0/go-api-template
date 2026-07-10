# Go API Template

> Please note that this project is still under development.

A template for building REST APIs in Go, backed by PostgreSQL. Fork it, rename it, and add your features. The security, testing, and release tooling is already wired.

## Who it's for

Teams starting a new Go backend who want solid security, testing, and release habits already in place. You get them wired in, not rebuilt each time. It well suits AI coding agents: the conventions are written down, so an agent can extend the code without guessing. You should know basic Go, and have Podman and `make` installed.

The rest of this file is reference material. The [Quick start](#quick-start) gets you running. The sections below cover configuration, deployment, and conventions in depth.

---

## Contents

1.  [Project layout](#project-layout)
2.  [Quick start](#quick-start)
3.  [Forking the template](#forking-the-template)
4.  [Configuration](#configuration)
5.  [Adding a feature](#adding-a-feature)
6.  [Authentication and Authorisation](#authentication-and-authorisation)
7.  [Database](#database)
8.  [Pagination](#pagination)
9.  [Migrations](#migrations)
10. [Observability](#observability)
11. [HTTP security](#http-security)
12. [Deployment requirements](#deployment-requirements)
13. [Coding rules](#coding-rules)
14. [Scaling](#scaling)
15. [Releases](#releases)
16. [Continuous integration](#continuous-integration)
17. [Makefile reference](#makefile-reference)
18. [Changelog](#changelog)

---

## Project layout

```
.
├── api/v1/openapi.yaml              OpenAPI spec
├── cmd/
│   ├── api/                         API server entrypoint (main + --version/--healthcheck CLI)
│   └── migrate/                     go-api-migrator, embedded migrator binary
├── internal/
│   ├── config/                      All env vars in one typed struct (incl. MigratorConfig)
│   ├── db/                          pgxpool wrapper + PgxIface + WithTransaction
│   ├── httpclient/                  Outbound HTTP client (propagates X-Request-ID)
│   ├── metrics/                     HTTP / pool / query / API-error metrics + pgx.QueryTracer
│   ├── middleware/                  CORS, security headers, request logger
│   ├── migrate/                     Migrator: destructive gate + pgx ConnConfig builder
│   ├── shared/                      Limits, validation, rune validators, logger
│   ├── userlog/                     Reference feature package, copy this for new features
│   └── version/                     Build-time version metadata (ldflags)
├── migrations/                      SQL migrations + embed.FS
├── scripts/                         extract-changelog.sh and its Go tests
├── tests/                           Pretty test-output runner
├── .github/workflows/               release.yml (GoReleaser + SLSA L3), ci.yml
├── .goreleaser.yaml                 Release pipeline config
├── CHANGELOG.md                     Hand-written, drives release notes
├── compose.dev.yaml                 Local Postgres + app (Air hot reload)
├── container.dev / container.prod   Dev (Air) / Prod (distroless multi-stage)
└── Makefile                         All dev commands
```

The reference feature package is `userlog` (not `log`) because the stdlib already has a `log` package. When you copy it, pick a name that doesn't shadow a stdlib package.

---

## Quick start

You need [Go](https://go.dev/dl/), [Podman](https://podman.io/), and `make`. From the project root:

```bash
cp .env.example .env   # the defaults work for local development
make setup             # install git hooks and build the containers
make run               # start PostgreSQL and the API, with hot reload
```

Confirm it works:

```bash
curl http://localhost:8080/livez    # {"status":"alive"}   the API is running
curl http://localhost:8080/readyz   # {"status":"healthy"} it can reach the database
```

Every route is protected by default, so a business call returns `401` until you add authentication:

```bash
curl http://localhost:8080/api/v1/logs   # {"error":"unauthorised", ...}
```

The app opens two ports. Port `8080` serves the API and its health checks. Port `9090` is an internal admin port that mirrors the health checks. It has no login, so keep it off the public internet by using a firewall or a private network. The app serves no metrics scrape endpoint. It pushes metrics and traces to an OpenTelemetry Collector instead (see [Observability](#observability)).

Forking this for your own project? Do the module rename first. See [Forking the template](#forking-the-template).

---

## Forking the template

When you fork this template for a real project, rename the Go module path once, across the whole repo:

```bash
git grep -l 'github.com/sud0x0/go-api-template' \
  | xargs sed -i 's|github.com/sud0x0/go-api-template|github.com/your/repo|g'
```

Rename in **every** tracked file, not just `*.go`. The module path is also a `-ldflags -X` version-injection target in `.goreleaser.yaml` and `container.prod`, and it appears in doc comments (`internal/version/version.go`). Go silently drops an unmatched `-X` flag, so a `*.go`-only rename leaves every forked release and container reporting `dev`/`unknown` versions with no build error. (The old path is not in the changelog. It was reset at 0.1.0, and the rename history lives in git.)

---

## Configuration

All env vars are read once at startup by [`internal/config`](internal/config/config.go) into a typed `Config`. There are no scattered `os.Getenv` calls anywhere else. Malformed numeric values (`DB_MAX_OPEN_CONNS=abc`) error at startup with the variable name and the bad value. There is no silent fallback.

### Required

| Variable                                                  | Notes                 |
| --------------------------------------------------------- | --------------------- |
| `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME` | PostgreSQL connection |

### Optional

| Variable                          | Default      | Notes                                                                                             |
| --------------------------------- | ------------ | ------------------------------------------------------------------------------------------------- |
| `PORT`                            | `8080`       | Public API port                                                                                   |
| `METRICS_PORT`                    | `9090`       | Internal admin port, restrict at infra layer                                                      |
| `SERVER_READ_HEADER_TIMEOUT_SECS` | `5`          |                                                                                                   |
| `SERVER_READ_TIMEOUT_SECS`        | `10`         |                                                                                                   |
| `SERVER_WRITE_TIMEOUT_SECS`       | `65`         | Must be > `SERVER_REQUEST_TIMEOUT_SECS` (validated at startup)                                    |
| `SERVER_IDLE_TIMEOUT_SECS`        | `120`        |                                                                                                   |
| `SERVER_REQUEST_TIMEOUT_SECS`     | `60`         | Per-request handler timeout (chi Timeout)                                                         |
| `LOG_LEVEL`                       | `production` | `development` / `production` / `quiet` / `silent` (default is fail-safe `production`)             |
| `DB_SSLMODE`                      | `require`    | `require` encrypts but does **not** verify the server. Use `verify-full` + a CA across untrusted networks (see [Database](#database)) |
| `DB_MAX_OPEN_CONNS`               | `100`        | pgxpool MaxConns                                                                                  |
| `DB_MIN_CONNS`                    | `10`         | pgxpool MinConns (eagerly maintained)                                                             |
| `DB_CONN_MAX_LIFETIME_MINS`       | `5`          |                                                                                                   |
| `DB_CONN_MAX_IDLE_TIME_MINS`      | `10`         |                                                                                                   |
| `TRUST_PROXY_HEADERS`             | `false`      | Trust `X-Forwarded-For`/`X-Real-IP` (`chimw.RealIP`). Enable only behind a trusted proxy.         |
| `RATE_LIMIT_RPM`                  | `0`          | App-level requests/min per IP. `0` disables (middleware not registered). Edge-less fallback only. |
| `PUBLIC_READINESS`                | `true`       | Serve `/readyz` + `/health` on the public listener. `false` keeps them internal-only.             |
| `CORS_ALLOWED_ORIGINS`            | (empty)      | Comma-separated. Never `*` in production.                                                         |
| `CORS_ALLOW_CREDENTIALS`          | `false`      | Set `true` only if browser clients send cookies                                                   |
| `MIGRATOR_LOCK_TIMEOUT`           | `5s`         | Postgres `lock_timeout` for the migration session                                                 |
| `MIGRATOR_STATEMENT_TIMEOUT`      | `10min`      | Postgres `statement_timeout` for the migration session                                            |
| `MIGRATOR_ALLOW_DESTRUCTIVE`      | `false`      | Gate for `down`/`down-to`/`redo`/`reset` (needs `--confirm-destructive` too)                      |

### Version banner

```
./bin/api --version
./bin/go-api-migrator --version
```

Both binaries print the same banner. For released builds the version metadata is injected by GoReleaser's `-ldflags` (see `.goreleaser.yaml`). A plain `go build` or `go run` falls back to `dev` / `unknown`.

---

## Adding a feature

Every feature is a self-contained package with no cross-feature imports. Copy [`internal/userlog/`](internal/userlog/) and rename:

```bash
cp -r internal/userlog internal/order
cd internal/order
for f in userlog_*.go; do mv "$f" "${f#userlog_}"; done   # → model.go, repository.go, etc.
sed -i 's/userlog/order/g; s/Log/Order/g' *.go
```

Each file owns one layer:

| File                    | Layer       | Owns                                                              |
| ----------------------- | ----------- | ----------------------------------------------------------------- |
| `*_model.go`            | Types       | Domain struct, request DTOs, `Validate()`                         |
| `*_errors.go`           | Errors      | Sentinel `Err*` + bounded `ErrType*` constants for metrics labels |
| `*_repository.go`       | Data        | SQL constants + struct backed by `db.PgxIface`                    |
| `*_service.go`          | Business    | Validation beyond schema and orchestration                        |
| `*_handler.go`          | HTTP        | Decode → null-byte strip → validate → service → encode            |
| `*_test.go`             | Unit        | pgxmock-backed                                                    |
| `*_integration_test.go` | Integration | Real Postgres, build tag `integration`                            |

Then:

1. Write the SQL migration: `migrations/NNNNN_*.sql` with `-- +goose Up` / `-- +goose Down` blocks.
2. Export `var RequiredTables = []string{"orders"}` from the feature's repository file and add `order.RequiredTables` to the `slices.Concat(...)` aggregation in [`cmd/api/main.go`](cmd/api/main.go). Startup refuses to boot if a required table is missing. (Schema verification is decoupled from `internal/db`. You never edit that core package.)
3. Wire constructors in [`cmd/api/main.go`](cmd/api/main.go):
   ```go
   orderRepo := order.NewOrderRepository(database.Pool())
   orderSvc  := order.NewOrderService(orderRepo)
   orderH    := order.NewOrderHandler(orderSvc, appLogger, appMetrics)
   ```
4. Register routes with a single line inside the existing `r.Route("/api/v1", …)` block: `orderH.Routes(r)` (each feature owns a `Routes(chi.Router)` method).
5. Document the endpoints in [`api/v1/openapi.yaml`](api/v1/openapi.yaml).
6. Add a `[Unreleased]` entry in [CHANGELOG.md](CHANGELOG.md).

### Layering rules

- **Handler:** HTTP only. Decodes the body, sanitises null bytes, runs struct validation, dispatches to the service, writes the response. No SQL, no business logic.
- **Service:** Business logic. No HTTP types. No SQL. Pure where possible.
- **Repository:** SQL only. Returns domain types or sentinel errors wrapped with multi-`%w` (sentinel + underlying driver error) so `errors.Is(err, ErrDatabase)` matches AND the cause is preserved for logging.
- **No cross-feature imports.** Shared concerns live in `internal/shared/`.

---

## Authentication and Authorisation

The template ships a **two-layer** auth model, both layers **disabled by default** so `make run` works with no IdP for local dev. Set the relevant environment variables to turn each on.

The app is an **OAuth2 resource server**. It receives a Bearer access token in the `Authorization` header, validates it, and enforces access. It does **not** run the login redirect, authorization-code exchange, PKCE, or the `nonce` flow. Those belong to your IdP and frontend. A resource server only validates the token it is handed.

### Layer 1: Authentication (OIDC)

On startup the middleware does OIDC discovery against `OIDC_ISSUER_URL` and builds a [`coreos/go-oidc`](https://github.com/coreos/go-oidc) verifier once. Per request it takes the `Authorization: Bearer <token>` header and verifies the token against the IdP's discovered JWKS. It rejects a missing or invalid token with a 401 in the JSON envelope and a `WWW-Authenticate: Bearer` header.

What the verifier enforces (each cited by ASVS ID in [`auth_middleware.go`](internal/middleware/auth_middleware.go)): signature valid against the discovered keys, an explicit algorithm allowlist (RS256 and ES256, never `none`), key material only from the trusted JWKS (a token-supplied `jku`/`x5u`/`jwk` is ignored), `exp`/`nbf` validity, audience checked against `OIDC_AUDIENCE`, and the user identified from `iss` plus `sub` (never a mutable claim like email). The issuer must match `OIDC_ISSUER_URL` exactly. `oidc.InsecureIssuerURLContext` is not used.

**Set `OIDC_AUDIENCE` to this API's own audience, not a client_id.** It must be the resource/audience identifier your IdP stamps into **access** tokens for this API (for example `https://api.example.com`). Do **not** set it to a browser/SPA `client_id`: an OIDC **ID token's** `aud` *is* the `client_id`, so if `OIDC_AUDIENCE` equals a `client_id` the audience check can no longer tell an ID token from an access token (token-type confusion).

**Token type.** Only access tokens are accepted for authorisation. The audience check above is the primary guard (an ID token's `aud` is the client_id, which is not the API audience). Layered on top, the middleware positively rejects an ID token even when its audience matches: it rejects `token_use == "id"` (Cognito) and any token carrying an OIDC-Core ID-token-only claim (`nonce`, `at_hash`, or `c_hash`), none of which appear in an access token. A fork whose IdP needs still stricter checking (for example requiring the RFC 9068 `typ: at+jwt` header) should add it in the middleware.

**Internal UUID mapping.** The `user_id` column is `UUID NOT NULL`, but an IdP `sub` is often not a UUID (`auth0|12345`, an email, a numeric id). The middleware derives the internal id deterministically as `UUIDv5(fixed-namespace, iss|sub)`, so the same identity always maps to the same UUID with no lookup table. A real deployment may instead keep a `users` table mapping `(iss, sub)` to an internal id and resolve it in the middleware before `shared.WithUserID`. Either way `shared.UserIDFromContext` enforces the UUID shape at the single read point.

### Layer 2: Authorisation (OPA)

After authentication, the second middleware asks an [Open Policy Agent](https://www.openpolicyagent.org/) (OPA) sidecar over its REST Data API whether the request is allowed. It POSTs `{"input": {subject, roles, action, resource, method, target_user}}` to `OPA_URL` + `OPA_DECISION_PATH` and enforces the boolean result. `resource` is the chi **leaf** route pattern (for example `/api/v1/logs/{id}`), never the raw path, so the policy input stays bounded. For that leaf pattern to be resolved, the authorisation middleware runs **after** routing — it is wired as an inline-group (per-endpoint) middleware via [`middleware.AuthorizedRoutes`](internal/middleware/routing.go); a plain pre-routing `r.Use` would hand OPA the mount pattern `/api/v1/*` and deny every request. `target_user` names the owner the request acts on (see the ABAC section below).

This is **function-level and attribute-level** authorisation. It answers "may this caller, with these attributes, perform this action on this target owner". It does **not** replace the repository's `WHERE user_id = $N` SQL scoping, which stays as the **row-ownership** layer answering "does this specific row belong to that owner". The two layers are deliberately separate. Do not push row filtering into OPA.

**Fail closed.** Any error (timeout, non-200, malformed body, missing result) denies with a 403. Only an explicit `result == true` allows.

**Vendor-agnostic by mapping, deny by default.** IdP role and group claims are mapped to internal role names at the auth boundary (`mapClaimsToRoles` in the authentication middleware). The shipped mapping is **empty**: it trusts no IdP group/role name, so by default no token receives any internal role. This is deliberate — the starter policy grants global cross-user access to the internal role `admin`, and a verbatim passthrough would hand that to any token merely carrying a group named `admin`. Curate the explicit allowlist (`mapClaimsToRolesWith`) with the specific IdP names you trust to enable roles; self-access keeps working without any role. The starter policy ([`deploy/opa/policy.rego`](deploy/opa/policy.rego)) is written against internal roles like `admin`, so the IdP and the policy are independently swappable. The policy is bind-mounted for local dev. In production, serve it as a signed bundle from a remote bundle server. OPA runs as a **separate process** (a sidecar), mirroring the OTel Collector deployment style, never inside the app image.

### Authorisation model (ABAC via OPA)

Authorisation is **attribute-based access control** (ABAC) evaluated by OPA. Access depends on **attributes of the caller** (its subject and roles, taken from the validated token) and **attributes of the target object** (which owner the row belongs to), not on the route alone. By default a request is self-access: it acts on the caller's own rows. A request may name another owner with the `?user=<uuid>` query parameter, and that cross-user access is allowed only when the policy says so.

**It can work as RBAC.** A role is just one attribute, so a policy rule that checks only roles **is** RBAC. The shipped policy has both: a role-only rule (an `admin` may act on any target owner, which is RBAC-style) and a scoped attribute rule (a `team-lead` may act on a target only when the caller and target share a `team`, which is the ABAC style). You choose the style by how you write Rego, not by changing the engine. NIST SP 800-162 treats a role as a valid ABAC attribute, so RBAC is a special case of ABAC (see the [NIST ABAC guide](https://csrc.nist.gov/pubs/sp/800/162/upd2/final)).

**Level 2 cross-user read and write.** With an authorising policy, a privileged caller may read AND update AND delete another user's logs, decided per object by OPA. Create is always caller-owned (you author your own records), so it has no cross-user path. The target owner may come from the request, but the enforcement is layered: OPA authorises the (caller, target, action) tuple at the function/attribute level, and the repository still scopes every query to that target's `user_id`, so a row the target does not own is a clean 404, never a cross-owner leak.

**User accounts vs machine accounts.** Both authenticate with an OAuth token validated the same way (one issuer, one validation path). A user token's subject is a person. A machine uses the OAuth2 **Client Credentials** grant, and its subject is the client application, with no user behind it (see [OAuth 2.1](https://datatracker.ietf.org/doc/html/draft-ietf-oauth-v2-1)). Both are authorised by the same OPA policy against their token attributes: a machine client simply carries the roles or scopes its client was granted.

**Why no separate API tokens.** A bespoke API-key store would be a second credential system, with a second validation path and a second revocation story to get right. OWASP recommends using standard OAuth mechanisms over custom keys (see the [OWASP REST Security Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/REST_Security_Cheat_Sheet.html)). So this template keeps one IdP, two token types (user and machine), and one validation path, rather than inventing API keys.

**Auditability caveat.** ABAC is harder to audit than RBAC: because access depends on runtime attribute values, the question "who can access X" often has no static answer (again the [NIST ABAC guide](https://csrc.nist.gov/pubs/sp/800/162/upd2/final)). OPA reduces, but does not eliminate, this gap with policy tests (`opa test`), rule coverage (`opa test --coverage`), and [decision logs](https://www.openpolicyagent.org/docs/latest/management-decision-logs/) that record every decision with its inputs. Turn decision logs on in production so each cross-user grant is reviewable after the fact.

**The non-negotiable rule.** Every authorisation attribute (the caller's subject, roles, and any boundary attribute such as team) comes from the **validated token or a trusted store keyed by the token subject**, never from request input. The target user **may** be request-supplied because it only names *which* object, but the caller's right to act on it is decided by OPA, never asserted by supplying it. This is [OWASP API1:2023 Broken Object Level Authorization](https://owasp.org/API-Security/editions/2023/en/0xa1-broken-object-level-authorization/): the check must validate that the caller may act on the specific object, from a trusted source.

### Configuration

| Variable            | Layer | Default                     | Meaning                                                              |
| ------------------- | ----- | --------------------------- | ------------------------------------------------------------------- |
| `OIDC_ISSUER_URL`   | authn | unset (authn off)           | OIDC issuer / discovery base. Setting it enables authentication.    |
| `OIDC_AUDIENCE`     | authn | unset                       | This API's own audience identifier (the `aud` your IdP puts in access tokens), **not** a browser client_id. Required when auth is enabled. |
| `OIDC_ENABLED`      | authn | derived from issuer set     | Explicit on/off override.                                           |
| `OPA_URL`           | authz | unset (authz off)           | OPA sidecar base URL. Setting it enables authorisation.             |
| `OPA_DECISION_PATH` | authz | `v1/data/api/authz/allow`   | REST Data API path to the boolean allow decision.                   |
| `OPA_TIMEOUT_MS`    | authz | `100`                       | OPA call timeout in ms. Bounded to `(0, 60000]`. Fails closed fast. |
| `OPA_ENABLED`       | authz | derived from URL set        | Explicit on/off override.                                           |

The contract seam is unchanged: auth middleware stores the internal UUID via `shared.WithUserID(ctx, id)`, and handlers read it via `shared.UserIDFromContext(ctx)`. The key lives in `internal/shared` so infrastructure middleware never imports a feature package. To swap in your own auth, replace the middleware wired at the `r.Route("/api/v1", …)` block in `cmd/api/main.go`.

> **Browser forks: do not store tokens in `localStorage`.** It is readable by any script on the page, so an XSS turns into token theft. Prefer a same-site, `HttpOnly` cookie set by a backend-for-frontend, or in-memory storage. That backend-for-frontend is a separate backend that sits in front of this API and holds the tokens itself. It is not this API, so a frontend must not assume this service holds its tokens. See the [OWASP HTML5 Security Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/HTML5_Security_Cheat_Sheet.html#local-storage). This is a client concern. The resource server takes no position beyond this note.

### What is out of scope

The login redirect, authorization-code exchange, PKCE, `state`/`nonce`, redirect-URI registration, refresh-token rotation, consent, and back-channel logout are **not** implemented. They are authorization-server and browser-client concerns owned by your IdP and frontend, not the resource server.

---

## Database

[`internal/db`](internal/db/db.go) wraps `*pgxpool.Pool`. Repositories accept `db.PgxIface`, a three-method interface (`Exec`, `Query`, `QueryRow`) satisfied by both `*pgxpool.Pool` and `pgx.Tx`. The same repository code therefore runs inside and outside transactions, and is unit-testable with [pgxmock](https://github.com/pashagolub/pgxmock).

Native pgx auto-prepares and caches statements per connection, with no manual `Prepare`/`Close` bookkeeping anywhere.

### Connection security (`DB_SSLMODE`)

The default `DB_SSLMODE=require` **encrypts** the database link but does **not authenticate the server**: it performs no CA or hostname verification, so it does not stop a man-in-the-middle that presents its own certificate. It is a sensible default for a link that never leaves a trusted network (for example a private VPC or the local compose stack, which uses `disable`).

For any deployment where the database link crosses an untrusted network, set `DB_SSLMODE=verify-full` and provide a CA certificate (via `PGSSLROOTCERT`/the standard libpq environment) so the driver verifies both the certificate chain and the hostname. `verify-full` is the only mode that defends against an active MITM. The config default stays `require` so local development works without certificate material; harden it in production.

### Transactions

```go
err := database.WithTransaction(ctx, func(tx pgx.Tx) error {
    // every operation atomic
    return nil
})
```

### Schema verification at startup

`db.New()` checks that every table in the `requiredTables` slice it is passed exists. The slice is aggregated in `cmd/api/main.go` via `slices.Concat(userlog.RequiredTables, …)`. Each feature exports its own `RequiredTables`, so `internal/db` is never edited when a feature is added. If any table is missing, the process exits with a clear "run `make db-migrate`" message.

---

## Pagination

`GET /api/v1/logs` supports two pagination modes. They are **mutually exclusive**. Supplying both `cursor` and `offset` is a `400 invalid_pagination`.

### Which to use

|                          | **Cursor (keyset)**, recommended                 | **Offset**, legacy / shallow                                 |
| ------------------------ | ------------------------------------------------ | ------------------------------------------------------------ |
| Query                    | `?cursor=` (first page), then echo `next_cursor` | `?limit&offset`                                              |
| Response                 | `{"logs":[…],"next_cursor":"…"}`                 | bare array `[…]`                                             |
| Cost                     | constant per page (index seek)                   | grows with depth (walk-and-discard)                          |
| Under concurrent inserts | stable, no shifted/duplicated rows               | pages shift, rows can repeat or be skipped                   |
| Depth                    | unbounded                                        | **hard cap at `offset` 10000**, deeper rows are unreachable  |

**Use cursor pagination for anything that can exceed a few thousand rows or is read while being written.** Offset stays for shallow human browsing (jump to page N) and backwards compatibility, but its `10000` cap is a hard reachability limit, not a soft default. Past it, switch to cursor.

### Cursor mechanics

- The first cursor page is requested with an empty value: `GET /api/v1/logs?cursor=&limit=50`. Each response carries `next_cursor`. Pass it back as `?cursor=<token>` for the next page. `next_cursor` is **omitted on the last page**.
- The cursor is **opaque**: base64url of the last row's keyset `(date_and_time, id)`. Treat it as a token. Do not construct or parse it client-side.
- Ordering is `date_and_time DESC, id DESC`. `id` is the tiebreaker so rows that share a timestamp paginate deterministically (the offset query uses the same order and the same `(user_id, date_and_time DESC, id DESC)` index, migration `00002`).
- A forged or tampered cursor **cannot cross user boundaries**: every query stays scoped by `user_id`. Cursor validation (strict RFC3339Nano + UUID parse, → `400` on anything malformed) exists to keep cast-error 500s and pathological values out of SQL, not for authorisation.

### Copying the pattern into a new feature

`internal/userlog` is the reference: the keyset SQL (`getLogsKeysetFirst` / `getLogsKeysetAfter` in `*_repository.go`), the opaque cursor codec (`*_cursor.go`), and the service's `getLogsCursor` (decode → query `limit+1` → emit `next_cursor`). Mirror those, and add the composite `(user_id, <sort_col> DESC, id DESC)` index in your migration.

---

## Migrations

The migrator is a separate binary at [`cmd/migrate`](cmd/migrate/main.go) (`go-api-migrator`, built by GoReleaser. Use `make prod-build` for a local snapshot). It embeds every `.sql` file under `migrations/` and applies them through goose's Provider API, serialised by a Postgres advisory lock.

### Local development

```bash
make db-migrate   # apply pending
make db-status    # show applied / pending
make db-reset     # roll back all  (dev-only, destructive gates inline)
```

Internally each target execs `go run ./cmd/migrate …` inside the app container so it inherits the compose-provided env.

### Production

Run the migrator BEFORE deploying the new API binary. The API checks the schema at startup and refuses to start if a table is missing.

```bash
DB_HOST=… DB_USER=… DB_PASSWORD=… DB_NAME=… ./go-api-migrator up
DB_HOST=… DB_USER=… DB_PASSWORD=… DB_NAME=… ./go-api
```

Subcommands:

| Command                                   | Effect                      |
| ----------------------------------------- | --------------------------- |
| `up` / `up-by-one` / `up-to <v>`          | Apply migrations            |
| `down` / `down-to <v>` / `redo` / `reset` | Roll back, **destructive**  |
| `status` / `db-version`                   | Read schema state           |

Destructive commands require BOTH gates:

```bash
MIGRATOR_ALLOW_DESTRUCTIVE=true go-api-migrator --confirm-destructive down
```

### Operational rules

1. **Expand / contract.** Every migration must be backwards-compatible with the currently-deployed API binary (rolling deploys run old code against new schema). Add columns as nullable or with defaults. Drop columns only in a later release after no deployed binary reads them. Never rename in place: add, dual-write, drop later.
2. **Direct Postgres connection required.** The session locker uses `pg_advisory_lock`, which is per-session. PgBouncer transaction/statement pooling breaks this. Use session pooling or connect directly.
3. **Tune timeouts.** `MIGRATOR_LOCK_TIMEOUT` (default `5s`) keeps a stalled lock acquire from blocking production writes. `MIGRATOR_STATEMENT_TIMEOUT` (default `10min`): raise for migrations with data backfills, and document the expected duration in the SQL file's header comment.
4. **Never edit an applied migration.** Once a migration has run against any shared/production database, fixes go in a new file. Editing an applied file silently de-syncs `goose_db_version` across environments.

---

## Observability

The model is OpenTelemetry (OTel) with one deliberate split:

- **Logs** are stdlib `log/slog` JSON to stdout. Transport is unchanged. The OTel Go logs SDK is still beta, so logs stay on slog rather than move in-process. A log line correlates to a trace by carrying `trace_id` and `span_id` (see [Logs](#logs)).
- **Traces and metrics** are exported over OTLP to a local [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/). The Collector batches them and re-exposes Prometheus for scraping.

Why this shape: the app stays cheap. It does no in-process batching beyond the SDK queue and serves no scrape endpoint. There is one operational model for both signals, and it is vendor-neutral because the Collector is the only place a backend is chosen. Prometheus survives only at the Collector edge, not inside the app.

When `OTEL_EXPORTER_OTLP_ENDPOINT` is unset, telemetry is disabled: the app installs no-op providers, boots and serves normally, and logs one line saying so. Set the endpoint (compose.dev.yaml already points it at the bundled Collector) to turn the pipeline on.

### Metrics

Metrics are pushed to the Collector over OTLP. The app no longer serves `/metrics`. To read metrics locally, scrape the Collector's Prometheus exporter at `localhost:8889/metrics`.

Instrument names follow OTel semantic conventions (semconv) where one exists. The Collector's Prometheus exporter lower-snakes the dotted names and adds unit and `_total` suffixes, so `http.server.request.duration` is scraped as `http_server_request_duration_seconds` and `api.errors` as `api_errors_total`. The tables below list the OTel instrument names.

The HTTP middleware is **panic-safe**: if a handler panics, a single deferred block still records the request (status `500` if no status was written, else the written status), restores the in-flight counter, and re-panics so `chi.Recoverer` upstream still produces the 500 response.

#### HTTP

| Instrument (OTel name)           | semconv? | Type              | Attributes                                                 |
| -------------------------------- | -------- | ----------------- | ---------------------------------------------------------- |
| `http.server.request.count`      | no       | Counter           | http.request.method, http.route, http.response.status_code |
| `http.server.request.duration`   | yes      | Histogram (s)     | http.request.method, http.route, http.response.status_code |
| `http.server.active_requests`    | yes      | UpDownCounter     | http.request.method                                        |
| `http.server.response.body.size` | yes      | Histogram (bytes) | http.request.method, http.route, http.response.status_code |

`http.route` is the chi matched route pattern (`/logs/{id}`) for matched routes or the literal `unmatched` for 404s. Cardinality stays bounded regardless of URL shape.

#### Database pool

Reported by an async OTel callback that reads `pool.Stat()` once at each metric collection (no background polling), preserving the original read-at-collection design.

| Instrument                                                                                 | Type               |
| ------------------------------------------------------------------------------------------ | ------------------ |
| `db.pool.acquired_conns`, `db.pool.idle_conns`, `db.pool.total_conns`, `db.pool.max_conns` | Observable gauge   |
| `db.pool.acquire_count`, `db.pool.acquire_duration_seconds`                                | Observable counter |
| `db.pool.empty_acquire_count`, `db.pool.canceled_acquire_count`                            | Observable counter |

#### Database queries

Reported by a `pgx.QueryTracer` wired into the pool's `ConnConfig.Tracer`. Every query is instrumented automatically.

| Instrument                     | semconv? | Type          | Attribute         |
| ------------------------------ | -------- | ------------- | ----------------- |
| `db.client.operation.duration` | yes      | Histogram (s) | db.operation.name |
| `db.client.operation.errors`   | no       | Counter       | db.operation.name |

`db.operation.name` is bounded to `SELECT`, `INSERT`, `UPDATE`, `DELETE`, `BEGIN`, `COMMIT`, `ROLLBACK`, `WITH`. Anything else maps to `OTHER`. `pgx.ErrNoRows` is excluded from the error counter because it is a normal `QueryRow` outcome, not a database error.

#### API errors

| Instrument   | Type    | Attributes          |
| ------------ | ------- | ------------------- |
| `api.errors` | Counter | feature, error_type |

Both attributes come from package-level Go constants (`FeatureName` and the `ErrType*` constants in each feature's `errors.go`). `err.Error()` text is **never** used as an attribute value.

### Traces

Each inbound request gets a server span from `otelhttp`, which wraps the public router outside chi so the chi middleware order is untouched. The span is exported over OTLP with the metrics. Because the span is on the request context before chi runs, the request logger can read its IDs and stamp them onto every log line (see below), and downstream instrumentation can attach to it. `otelhttp` here is configured for spans only. A no-op meter provider is passed so it does not emit its own HTTP metrics, which would collide with the ones the metrics middleware records.

### Logs

Every line is JSON to stdout (no log files, no rotation in the app, the orchestrator does that). The shape is:

```json
{
  "time": "2026-06-07T15:30:00+10:00",
  "level": "INFO",
  "msg": "creating log entry",
  "service": "go-api",
  "version": "1.2.3",
  "commit": "abc1234",
  "request_id": "req-xyz",
  "method": "POST",
  "path": "/api/v1/logs",
  "ip": "203.0.113.5:1234",
  "user_id": "u-42",
  "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
  "span_id": "00f067aa0ba902b7"
}
```

| Field                                | Source                                                                                                                 | When present                                                                  |
| ------------------------------------ | ---------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| `time`                               | slog default, formatted RFC3339                                                                                        | every line                                                                    |
| `level`, `msg`                       | slog default                                                                                                           | every line                                                                    |
| `service`, `version`, `commit`       | `NewLogger(level, serviceName)`, bound once via `WithAttrs`, and version/commit come from `internal/version` (`-ldflags`) | every line                                                                    |
| `request_id`, `method`, `path`, `ip` | `RequestLogger` middleware via `WithRequestContext`                                                                    | every line emitted during an HTTP request                                     |
| `trace_id`, `span_id`                | `RequestLogger` reads the active OTel span from the request context                                                    | every line during a request with a valid span (omitted when telemetry is off) |
| `user_id`                            | re-bound by an auth middleware (recommended pattern)                                                                   | every line emitted _after_ auth fires                                         |
| `error`, `actual_error`              | only on `LogError`                                                                                                     | error lines                                                                   |
| `status`, `duration_ms`              | only on the "request completed" line                                                                                   | end-of-request lines                                                          |

**One log line is enough to identify the request, the route, the client, and (after auth) the user.** A handler that returns an internal error logs:

```json
{"…", "level":"ERROR", "msg":"error occurred",
 "service":"go-api", "version":"1.2.3", "commit":"abc1234",
 "request_id":"req-xyz", "method":"GET", "path":"/api/v1/logs/{id}",
 "ip":"203.0.113.5:1234", "user_id":"u-42",
 "error":"database error",
 "actual_error":"getLog abc: database error: pq: connection refused"}
```

No correlation join needed for the request itself: every field to triage the incident is on that single line. To jump from a log line to its distributed trace, take `trace_id` and `span_id` and look them up in your tracing backend (fed by the Collector). That is the whole point of stamping them onto the line.

**Wiring user_id (when you add auth):** in your auth middleware, after you've validated the token and pulled the user ID, re-wrap the request-scoped logger:

```go
log := logger.FromContext(r.Context(), defaultLogger)
log = log.WithRequestContext(logger.RequestContext{UserID: userID})
ctx = context.WithValue(r.Context(), logger.LoggerContextKey, log)
```

`WithRequestContext` only adds non-empty fields, so you don't lose the request_id/method/path/ip already bound. They stay, and user_id joins them.

### Health endpoints

| Endpoint  | Purpose                                      | Use for                                    |
| --------- | -------------------------------------------- | ------------------------------------------ |
| `/livez`  | Process responsiveness, no dependency check  | k8s `livenessProbe`                        |
| `/readyz` | Dependency check (DB ping), 503 if unhealthy | k8s `readinessProbe`, load-balancer health |
| `/health` | Alias for `/readyz`                          | Backwards-compatibility                    |

`/readyz` and `/health` are mirrored on the internal admin port and, on the public listener, are gated by `PUBLIC_READINESS` (default `true`). The readiness result is cached for ~2s (concurrency-safe), and the cached DB ping runs on a context detached from the requesting client's cancellation, so a flood of probes collapses to at most one DB ping per window and a client aborting `/readyz` mid-ping cannot poison the shared result. Health endpoints (`/livez`, `/readyz`, `/health`) are **exempt** from the app-level rate limiter (a low `RATE_LIMIT_RPM` must never 429 liveness probes into a restart loop). They rely on the readiness cache and edge controls instead. Trade-off: exposing readiness publicly is convenient for external load-balancer health checks but discloses operational state and adds a (now-bounded) DB-ping vector. Set `PUBLIC_READINESS=false` to keep it internal-only.

### Configuration

| Variable                      | Default  | Effect                                                                                                                                      |
| ----------------------------- | -------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | (unset)  | OTLP/HTTP Collector endpoint (for example `http://otel-collector:4318`). Unset means telemetry is disabled.                                 |
| `OTEL_SERVICE_NAME`           | `go-api` | `service.name` resource attribute, matching the logger's `service` field.                                                                   |
| `OTEL_ENABLED`                | derived  | When unset, defaults to "enabled if an endpoint is set". Set `false` to force-disable, or `true` to fail fast when the endpoint is missing. |

With the endpoint unset the app runs with tracing and metrics as no-ops and logs one line saying telemetry is disabled. It still serves requests and health checks normally, so a fork with no Collector boots out of the box.

### Deployment

The Collector runs as a **separate process**, never inside the app image. Pick a [deployment pattern](https://opentelemetry.io/docs/collector/deploy/) to suit your platform: a sidecar next to each pod, a per-node agent, or a central gateway. `compose.dev.yaml` bundles one for local development (config in [`deploy/otel-collector-config.yaml`](deploy/otel-collector-config.yaml)) that receives OTLP, exports to a debug logger, and re-exposes Prometheus on `localhost:8889/metrics`. See [Deployment requirements](#deployment-requirements).

### What's NOT included

Prometheus, Grafana, and alertmanager are deliberately separate concerns. The app is a telemetry _producer_ that pushes OTLP to the Collector. The Collector re-exposes Prometheus at its edge, and the monitoring stack (Prometheus server, dashboards, alert rules) should live in a separate infrastructure repo. Dashboard JSON and alert rules change for different reasons than business logic.

---

## HTTP security

| Header                    | Value                                        | Purpose                             |
| ------------------------- | -------------------------------------------- | ----------------------------------- |
| `Content-Security-Policy` | `default-src 'none'; frame-ancestors 'none'` | Strict CSP for a JSON API           |
| `X-Content-Type-Options`  | `nosniff`                                    | Prevent MIME sniffing               |
| `Cache-Control`           | `no-store`                                   | Don't cache authenticated responses |
| `Referrer-Policy`         | `no-referrer`                                | Prevent URL/token leakage           |

### CORS

Configured via `CORSConfig` in main.go. Origins from `CORS_ALLOWED_ORIGINS` (comma-separated, explicit origins only). The middleware matches origins exactly, so a wildcard entry is silently inert. **Startup rejects any `*`** (and rejects `CORS_ALLOW_CREDENTIALS=true` with an empty origin list, which can never succeed). `Vary: Origin` is set on every response so shared caches don't poison. `Access-Control-Allow-Credentials: true` is emitted only when `CORS_ALLOW_CREDENTIALS=true`.

### Client IP (`X-Forwarded-For`)

`chimw.RealIP` resolves `X-Forwarded-For` / `X-Real-IP` onto `r.RemoteAddr` BEFORE the request logger, so the logged `ip` field is the resolved client IP, not the upstream proxy.

> **Trust assumption:** `X-Forwarded-For` is client-spoofable. `RealIP` is therefore **opt-in** via `TRUST_PROXY_HEADERS` (default `false`) and is only registered when the app sits behind a trusted reverse proxy or load balancer that overwrites these headers on every request. With the default `false` the headers are ignored and `r.RemoteAddr` is the real peer, safe for an app exposed directly to the internet. Leaving it off while spoofable headers are honoured would also poison any IP-keyed rate limiter (see below).

### Handled at the infrastructure layer

- **HSTS**: at the reverse proxy or load balancer (TLS terminates outside the app).
- **Rate limiting**: primarily at the proxy / API gateway / WAF (see [Deployment requirements](#deployment-requirements)). An opt-in app-level fallback exists (`RATE_LIMIT_RPM`) for edge-less deployments, disabled by default.

## Deployment requirements

This template expects these controls to live **outside the application**, not in it:

1. **TLS termination + HSTS.** TLS terminates outside the app. The app speaks plain HTTP behind the edge.
2. **Rate limiting.** The edge is the **primary** rate-limit control: it sees the true client IP, holds a cluster-wide view, and can shed load before it reaches the app.
3. **An OpenTelemetry Collector.** Traces and metrics are pushed via OTLP, so a Collector must be reachable at `OTEL_EXPORTER_OTLP_ENDPOINT`. Run it as a separate process (sidecar, per-node agent, or gateway per the [Collector deployment guide](https://opentelemetry.io/docs/collector/deploy/)), never inside the app image. If the endpoint is unset the app still runs with telemetry disabled.

The app ships an **opt-in fallback limiter** (`RATE_LIMIT_RPM`, default `0` = disabled, keyed by client IP) for deployments that genuinely have no edge limiter. Two caveats make it a fallback, not a replacement:

- **Per-instance counters.** `httprate` counts in memory per process, so with `N` replicas the effective global limit is `RATE_LIMIT_RPM × N`. A true global limit belongs at the edge.
- **IP keying needs trusted proxy headers.** If the app is behind a proxy but `TRUST_PROXY_HEADERS=false`, every request carries the proxy's IP, so all traffic shares one bucket and the limiter self-DoSes the API. The app logs a startup warning when `RATE_LIMIT_RPM > 0` while `TRUST_PROXY_HEADERS=false`.

The limiter applies only to business routes (`/api/v1/*` and any future routes added to that group). The health endpoints (`/livez`, `/readyz`, `/health`) are exempt so probes are never throttled into a restart loop. When the limiter fires it returns `429` with the standard JSON error envelope (`{"error":"rate_limited",…}`). In edge-limited deployments the `429` may instead come from the upstream gateway, whose body shape is its own.

**After auth lands:** re-key the limiter by user ID for business-aware, per-endpoint limits. The per-IP key is a coarse pre-auth stopgap.

---

## Coding rules

1. **Validate strictly, parameterise queries, encode on output.** The API stores exactly what the client sent. XSS prevention lives at the renderer. The API relies on `json.Encoder`'s HTML escaping (`<` → `<` etc.) plus parameterised pgx queries. The only input cleaning is `shared.SanitiseNullBytes` (Postgres `TEXT` cannot store `\x00`).
2. **Type and validate inputs.** UUID path params via `uuid.Parse` so a malformed id never reaches the database. Length limits via `rune_max` / `rune_min` / `rune_len` tags. Rune count matches Postgres `VARCHAR(N)` semantics.
3. **Validate database output.** Every row read from Postgres is re-checked via `Model.Validate()` (defence in depth: a schema migration drift would surface here, not later in business logic).
4. **Authenticate by default.** Every API endpoint requires authentication unless explicitly excluded.
5. **Authorise in the repository.** Every query has `WHERE … AND user_id = $N`. No filtering in the handler that the database can do.
6. **Validate file uploads.** MIME type, file extension, size limit. None are implemented in the template. Add them when you add an upload endpoint.

> Validate on input, parameterise on storage, encode on output, at each language boundary, using the tool that owns that boundary. Input sanitisation sits _below_ those three in OWASP's defence hierarchy ([Input Validation](https://cheatsheetseries.owasp.org/cheatsheets/Input_Validation_Cheat_Sheet.html) and [XSS Prevention](https://cheatsheetseries.owasp.org/cheatsheets/Cross_Site_Scripting_Prevention_Cheat_Sheet.html)): use it only when the destination language structurally cannot hold the input. This codebase's only case is `shared.SanitiseNullBytes`, because Postgres `TEXT` rejects `\x00`. Never use sanitisation as a substitute for validation, never to "neutralise" XSS (that belongs at the renderer, in the renderer's templating engine), and never as a "be safe" reflex on every string. Sanitisation silently mutates user data, gives false confidence in places where the actual defence is elsewhere, and when applied repeatedly across round-trips causes double-encoding bugs that are painful to debug.

---

## Scaling

This template follows the [12-factor](https://12factor.net/concurrency) process model: scale by running **more processes**, not by making one process bigger. The API is stateless, so horizontal scale is `N` identical instances behind a load balancer. No session affinity needed.

### Sizing the connection pool

`DB_MAX_OPEN_CONNS` is **per instance**, not cluster-wide. If you run 4 instances with `DB_MAX_OPEN_CONNS=25`, you have a 100-connection ceiling at Postgres. Set Postgres' `max_connections` accordingly (plus headroom for the migrator and any maintenance sessions).

`DB_MIN_CONNS` controls how many connections each instance eagerly warms up. With 4 instances at `DB_MIN_CONNS=10`, you have 40 idle connections waiting at all times. Tune low if you scale aggressively up and down. Tune high if your traffic is steady and you want first-request latency to be low.

### When you need a worker process type

If you have **async work** (sending emails, processing webhooks, retrying failed external calls, batch jobs, scheduled tasks), do **not** run them inside the API process. Two reasons:

1. The job blocks the HTTP request that triggered it. Slow jobs mean slow API responses.
2. The job does not survive `SIGTERM` during a deploy. Mid-execution jobs are lost unless your queue handles redelivery.

The 12-factor answer: a second process type. Add a `cmd/worker/main.go` that runs alongside the API, shares `internal/config`, ships through the same GoReleaser pipeline, and consumes jobs from a queue.

### The Redis pattern (concrete recipe)

Most teams reach for Redis here. The wiring is straightforward and follows the same patterns this template uses everywhere else.

#### 1. Add Redis to config

Extend `internal/config/config.go` the way you'd add any other backing service:

```go
type Config struct {
    // … existing fields
    Redis RedisConfig
}

type RedisConfig struct {
    URL      string
    PoolSize int
}

// In Load():
Redis: RedisConfig{
    URL:      getEnv("REDIS_URL", ""),
    PoolSize: redisPoolSize,  // from getEnvInt("REDIS_POOL_SIZE", 10)
}
```

Add `REDIS_URL` + `REDIS_POOL_SIZE` to `.env.example`. No `os.Getenv` outside `internal/config`.

#### 2. Pick a queue library

| Need                                                     | Use                                                                                                       |
| -------------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| Job queue with retries, scheduled, priority, idempotency | [`hibiken/asynq`](https://github.com/hibiken/asynq) (Redis-backed, production-ready, ships with a web UI) |
| Lightweight pub/sub or streams                           | [`redis/go-redis/v9`](https://github.com/redis/go-redis) directly with Redis Streams                      |
| Distributed cache / rate limiting                        | `redis/go-redis/v9`, no queue layer needed                                                                |

This template doesn't include any of them by default. Add what you actually need.

#### 3. Wire a producer in the API

The handler that triggers the job enqueues, then returns 202 to the client:

```go
// internal/email/email_handler.go
func (h *Handler) SendEmail(w http.ResponseWriter, r *http.Request) {
    // ... validate body ...
    task := asynq.NewTask("email:send", payload)
    if _, err := h.queue.Enqueue(task); err != nil {
        h.handleError(ctx, w, err)
        return
    }
    w.WriteHeader(http.StatusAccepted)
}
```

The queue client is injected into the handler the same way `HandlerMetrics` is, through the constructor, no globals.

#### 4. Add the worker binary

```
cmd/worker/main.go      # consumes jobs from Redis
internal/jobs/          # job handler functions, following the userlog layered pattern
```

`cmd/worker/main.go` mirrors `cmd/api/main.go`:

- Load config (same `config.Load()`, already reads `REDIS_URL`).
- Create the logger (`logger.NewLogger(cfg.Log.Level, "go-api-worker")`, note the distinct service name).
- Open the DB pool (same `db.New(...)`).
- Initialise telemetry and metrics (same `observability.Init(...)` then `metrics.New()`).
- Start an asynq.Server (or a Redis Streams consumer loop).
- Bind `/livez` + `/readyz` on `:METRICS_PORT` so k8s can probe it.
- Graceful shutdown on SIGINT/SIGTERM: drain in-flight jobs before exiting.

Job handler functions live in `internal/jobs/` and follow the same layered pattern as `internal/userlog/`: handler / service / repository. The handler in this case is the `asynq.HandlerFunc`, not an HTTP handler.

#### 5. Wire the worker into the release pipeline

Add a third build to `.goreleaser.yaml`:

```yaml
builds:
  - id: api # existing
    main: ./cmd/api
    binary: go-api
    # … same ldflags
  - id: migrator # existing
    main: ./cmd/migrate
    binary: go-api-migrator
    # … same ldflags
  - id: worker # new
    main: ./cmd/worker
    binary: go-api-worker
    # … same ldflags
```

GoReleaser produces all three binaries on the same release tag. Deploy them together so version skew is impossible.

### Cache use cases (not queues)

If you need Redis for caching (session data, rate-limit counters, computed responses) rather than a queue:

- Use `redis/go-redis/v9` directly, no queue layer needed.
- Wrap Get/Set/Del in a `internal/cache/` package so feature code doesn't know it's Redis.
- **Do not** cache in process memory (`sync.Map`, package-level maps). That breaks the stateless-process rule and your cache is per-instance, useless for sharing rate limits.
- Set explicit TTLs on every key. Unbounded caches grow until Redis OOMs.

### What to delete from the template if you don't need Redis

Most APIs do not need a worker process to start. Don't add Redis pre-emptively. The right time to add it is the first time you write code that thinks "this should happen later" or "this should retry". Until then, the API + migrator combination is the right shape.

---

## Releases

Releases are cut by pushing a `v*` tag. [`.github/workflows/release.yml`](.github/workflows/release.yml) runs GoReleaser, which produces:

- Static binaries for linux/darwin/windows × amd64/arm64 (windows/arm64 skipped), both `go-api` and `go-api-migrator`
- `checksums.txt`: SHA-256 over every binary
- `<binary>.sbom.json`: one SPDX-JSON SBOM per binary, generated by syft
- `multiple.intoto.jsonl`: SLSA Level 3 provenance, generated by the [`slsa-github-generator`](https://github.com/slsa-framework/slsa-github-generator) reusable workflow

### Release notes come from CHANGELOG.md

[`scripts/extract-changelog.sh`](scripts/extract-changelog.sh) extracts the `## [X.Y.Z]` section from [CHANGELOG.md](CHANGELOG.md) and passes it to GoReleaser via `--release-notes`. The workflow FAILS if the section is missing or empty. No tag gets released without a hand-written entry. **Commit messages do not feed the changelog.** There is no commit-format enforcement and no commit-derived changelog generation.

The trade-off you accept by choosing manual changelogs: every release costs a few minutes of writing. In exchange the changelog reflects what the binary user cares about, not what the commit graph happens to record.

### Cutting a release

```bash
# 1. Move the [Unreleased] notes into a new dated section locally.
$EDITOR CHANGELOG.md   # `## [Unreleased]` → `## [1.2.3] - 2026-04-12`
                       # restore a fresh empty `## [Unreleased]` above

# 2. Confirm.
make changelog-check VERSION=1.2.3
make goreleaser-check     # full local pre-flight

# 3. Tag.
git add CHANGELOG.md && git commit -m "release: 1.2.3"
git tag v1.2.3
git push origin main --tags
```

### Verifying downloads

```bash
sha256sum -c checksums.txt

slsa-verifier verify-artifact <binary> \
  --provenance-path multiple.intoto.jsonl \
  --source-uri github.com/<owner>/<repo> \
  --source-tag v1.2.3
```

---

## Continuous integration

[`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs on every pull request + push to `main`:

| Job                 | Catches                                                                                                                             |
| ------------------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| `pre-commit`        | Hooks bypassed locally with `--no-verify`, mandatory here. Pinned tool versions (golangci-lint, govulncheck, semgrep, pre-commit). |
| `test`              | `go build`, `go vet`, `go test -race`                                                                                               |
| `test-integration`  | Postgres 16 service container, `go run ./cmd/migrate up`, then `go test -race -tags integration ./...`                              |
| `goreleaser-config` | `goreleaser check` + `extract-changelog.sh` fixture tests: broken release config or script is caught on PR, not at tag time         |

### Pre-commit hooks (run locally + in CI)

| Hook                                                     | Catches                         |
| -------------------------------------------------------- | ------------------------------- |
| `gitleaks`                                               | Committed secrets               |
| `gofmt`                                                  | Formatting                      |
| `golangci-lint`                                          | Static analysis (incl. `gosec`) |
| `govulncheck`                                            | Known CVEs in dependencies      |
| `semgrep`                                                | Security anti-patterns          |
| `check-merge-conflict`                                   | Unresolved `<<<<<<<` markers    |
| `check-added-large-files`                                | Accidental binary blobs         |
| `check-yaml`, `trailing-whitespace`, `end-of-file-fixer` | Hygiene                         |

---

## Commit messages

Contributors and agents are asked to follow [Conventional Commits v1.0.0](https://www.conventionalcommits.org/en/v1.0.0/): `type(scope): summary`. A few repo-flavoured examples:

```text
feat(userlog): add atomic batch create endpoint
fix(db): bound pool sizes before the int32 conversion
build(make): split test targets from the ci umbrella
docs: document the verification loop
```

Breaking changes append `!` to the type/scope and add a `BREAKING CHANGE:` footer (the historical module rename would have been `refactor!:`).

This is a **request, not an enforced gate**. No hook or CI job rejects other formats, and history before this point predates the convention. The local gate is `make ci` (no podman) after every change and `make verify` (podman) before a commit. See the [Makefile reference](#makefile-reference).

---

## Makefile reference

### Setup

| Command        | Description                                              |
| -------------- | -------------------------------------------------------- |
| `make setup`   | First-time: copy `.env`, install hooks, build containers |
| `make run`     | Start the stack                                          |
| `make logs`    | Tail app logs                                            |
| `make destroy` | Remove containers, volumes, images                       |
| `make clean`   | Delete build/test/temp artefacts                         |

### Database

| Command           | Description                                        |
| ----------------- | -------------------------------------------------- |
| `make db-migrate` | Apply pending migrations (via embedded binary)     |
| `make db-status`  | Show applied / pending                             |
| `make db-reset`   | Roll back all (dev-only, destructive gates inline) |

### Tests + lint

| Command                               | Description                                                                               |
| ------------------------------------- | ----------------------------------------------------------------------------------------- |
| `make ci`                             | Umbrella gate (no podman): `go build` + `go vet` + `golangci-lint` + race unit tests      |
| `make verify`                         | Full pre-commit gate (podman): `ci` + integration tests                                   |
| `make test-unit`                      | Race-enabled unit tests (pgxmock, no real DB needed). `make test` is a deprecated alias. |
| `make test-pretty`                    | Table-formatted output over the same `-race` unit suite                                   |
| `make test-integration`               | Real-Postgres integration tests (requires `make run`)                                     |
| `make test-scripts`                   | Shell-script Go tests (extract-changelog fixture)                                         |
| `make lint` / `make fmt` / `make vet` | golangci-lint / gofmt / go vet                                                            |
| `make vulncheck` / `make semgrep`     | govulncheck / semgrep                                                                     |
| `make pre-commit-run`                 | Run every hook against every file                                                         |

### Release

There are no hand-rolled `build-binary` / `build-migrator` / `build-prod` targets. Release-shaped binaries come from a single source, GoReleaser, both locally (via the snapshot below) and in CI. `internal/version` is populated by GoReleaser's `-ldflags` only. The Makefile contains no `LDFLAGS` or version metadata, so the two paths can't drift.

| Command                              | Description                                                                                                                                             |
| ------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `make prod-build`                    | Local snapshot via GoReleaser: cross-compiled binaries in `dist/`, no SBOM (skip syft), no publish                                                      |
| `make goreleaser-check`              | Full release-pipeline pre-flight: config validate + extract-changelog tests + full snapshot + SLSA-subjects jq replay + native binary `--version` smoke |
| `make changelog-check VERSION=x.y.z` | Verify CHANGELOG has a non-empty section for the version (no leading `v`)                                                                               |

For a dev one-off binary just use `go build` or `go run` directly. Those don't carry release ldflags and aren't meant to.

---

## Changelog

See [CHANGELOG.md](CHANGELOG.md). The format is [Keep a Changelog](https://keepachangelog.com). The release workflow extracts the relevant section automatically. A release fails if it's missing.
