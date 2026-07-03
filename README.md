# Go API Template

Production-ready Go API template. Layered architecture (handler → service → repository), one package per feature. Native pgx v5, embedded SQL migrator with Postgres session locking, Prometheus-instrumented end-to-end, GoReleaser pipeline with SLSA Level 3 provenance and a manually-written changelog.

The [`internal/userlog`](internal/userlog/) package is the canonical feature. Copy it for every new feature.

---

## Contents

1.  [Project layout](#project-layout)
2.  [Quick start](#quick-start)
3.  [Configuration](#configuration)
4.  [Adding a feature](#adding-a-feature)
5.  [Authentication](#authentication)
6.  [Database](#database)
7.  [Pagination](#pagination)
7.  [Migrations](#migrations)
8.  [Observability](#observability)
9.  [HTTP security](#http-security)
10. [Deployment requirements](#deployment-requirements)
11. [Coding rules](#coding-rules)
11. [Scaling](#scaling)
12. [Releases](#releases)
13. [Continuous integration](#continuous-integration)
14. [Makefile reference](#makefile-reference)
15. [Changelog](#changelog)

---

## Project layout

```
.
├── api/v1/openapi.yaml              OpenAPI spec
├── cmd/
│   ├── api/                         API server entrypoint (main + --version/--healthcheck CLI)
│   └── migrate/                     go-api-migrator — embedded migrator binary
├── internal/
│   ├── config/                      All env vars in one typed struct (incl. MigratorConfig)
│   ├── db/                          pgxpool wrapper + PgxIface + WithTransaction
│   ├── httpclient/                  Outbound HTTP client (propagates X-Request-ID)
│   ├── metrics/                     HTTP / pool / query / API-error metrics + pgx.QueryTracer
│   ├── middleware/                  CORS, security headers, request logger
│   ├── migrate/                     Migrator: destructive gate + pgx ConnConfig builder
│   ├── shared/                      Limits, validation, rune validators, logger
│   ├── userlog/                     Reference feature package — copy this for new features
│   └── version/                     Build-time version metadata (ldflags)
├── migrations/                      SQL migrations + embed.FS
├── scripts/                         extract-changelog.sh and its Go tests
├── tests/                           Pretty test-output runner
├── .github/workflows/               release.yml (GoReleaser + SLSA L3), ci.yml
├── .goreleaser.yaml                 Release pipeline config
├── CHANGELOG.md                     Hand-written; drives release notes
├── compose.dev.yaml                 Local Postgres + app (Air hot reload)
├── container.dev / container.prod   Dev (Air) / Prod (distroless multi-stage)
└── Makefile                         All dev commands
```

The reference feature package is `userlog` (not `log`) because the stdlib already has a `log` package. When you copy it, pick a name that doesn't shadow a stdlib package.

---

## Quick start

```bash
# 1. Rename the module (once, when you fork the template). One repo-wide sweep
#    over EVERY tracked file — nothing is excluded. (The module-rename history
#    lives in git, not the CHANGELOG: the changelog was reset at 0.1.0 and no
#    longer carries the old path verbatim — see its reset notice.)
git grep -l 'github.com/sud0x0/go-api-template' \
  | xargs sed -i 's|github.com/sud0x0/go-api-template|github.com/your/repo|g'
# Why repo-wide, not just *.go: the module path is also a `-ldflags -X`
# version-injection target in .goreleaser.yaml and container.prod, and appears
# in doc comments (internal/version/version.go) — not only in Go imports.
# Go DROPS an unmatched -X flag SILENTLY, so a *.go-only rename leaves every
# forked release/container reporting dev/unknown versions with no build error.

# 2. First-time setup: copies .env.example to .env, installs pre-commit hooks, builds containers.
make setup

# 3. Daily: start the stack.
make run

# 4. Verify.
curl http://localhost:8080/livez    # process responsive
curl http://localhost:8080/readyz   # process + database
curl http://localhost:9090/metrics  # Prometheus, on the internal admin port
```

The app exposes two listeners:

- **`:8080`** — public API + health endpoints
- **`:9090`** — internal admin (`/metrics`, mirrored `/livez` and `/readyz`)

Restrict the internal port at the infrastructure layer (NetworkPolicy / firewall / private network). It has no authentication.

---

## Configuration

All env vars are read once at startup by [`internal/config`](internal/config/config.go) into a typed `Config`. There are no scattered `os.Getenv` calls anywhere else. Malformed numeric values (`DB_MAX_OPEN_CONNS=abc`) error at startup with the variable name and the bad value — no silent fallback.

### Required

| Variable                                                  | Notes                 |
| --------------------------------------------------------- | --------------------- |
| `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME` | PostgreSQL connection |

### Optional

| Variable                          | Default       | Notes                                                                        |
| --------------------------------- | ------------- | ---------------------------------------------------------------------------- |
| `PORT`                            | `8080`        | Public API port                                                              |
| `METRICS_PORT`                    | `9090`        | Internal admin port — restrict at infra layer                                |
| `SERVER_READ_HEADER_TIMEOUT_SECS` | `5`           |                                                                              |
| `SERVER_READ_TIMEOUT_SECS`        | `10`          |                                                                              |
| `SERVER_WRITE_TIMEOUT_SECS`       | `65`          | Must be > `SERVER_REQUEST_TIMEOUT_SECS` (validated at startup)               |
| `SERVER_IDLE_TIMEOUT_SECS`        | `120`         |                                                                              |
| `SERVER_REQUEST_TIMEOUT_SECS`     | `60`          | Per-request handler timeout (chi Timeout)                                    |
| `LOG_LEVEL`                       | `production`  | `development` / `production` / `quiet` / `silent` (default is fail-safe `production`) |
| `DB_SSLMODE`                      | `require`     |                                                                              |
| `DB_MAX_OPEN_CONNS`               | `100`         | pgxpool MaxConns                                                             |
| `DB_MIN_CONNS`                    | `10`          | pgxpool MinConns (eagerly maintained)                                        |
| `DB_CONN_MAX_LIFETIME_MINS`       | `5`           |                                                                              |
| `DB_CONN_MAX_IDLE_TIME_MINS`      | `10`          |                                                                              |
| `TRUST_PROXY_HEADERS`             | `false`       | Trust `X-Forwarded-For`/`X-Real-IP` (`chimw.RealIP`). Enable only behind a trusted proxy. |
| `RATE_LIMIT_RPM`                  | `0`           | App-level requests/min per IP. `0` disables (middleware not registered). Edge-less fallback only. |
| `PUBLIC_READINESS`                | `true`        | Serve `/readyz` + `/health` on the public listener. `false` keeps them internal-only. |
| `CORS_ALLOWED_ORIGINS`            | (empty)       | Comma-separated. Never `*` in production.                                    |
| `CORS_ALLOW_CREDENTIALS`          | `false`       | Set `true` only if browser clients send cookies                              |
| `MIGRATOR_LOCK_TIMEOUT`           | `5s`          | Postgres `lock_timeout` for the migration session                            |
| `MIGRATOR_STATEMENT_TIMEOUT`      | `10min`       | Postgres `statement_timeout` for the migration session                       |
| `MIGRATOR_ALLOW_DESTRUCTIVE`      | `false`       | Gate for `down`/`down-to`/`redo`/`reset` (needs `--confirm-destructive` too) |

### Version banner

```
./bin/api --version
./bin/go-api-migrator --version
```

Both binaries print the same banner. For released builds the version metadata is injected by GoReleaser's `-ldflags` (see `.goreleaser.yaml`); a plain `go build` or `go run` falls back to `dev` / `unknown`.

---

## Adding a feature

Every feature is a self-contained package — no cross-feature imports. Copy [`internal/userlog/`](internal/userlog/) and rename:

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
| `*_service.go`          | Business    | Validation beyond schema; orchestration                           |
| `*_handler.go`          | HTTP        | Decode → null-byte strip → validate → service → encode            |
| `*_test.go`             | Unit        | pgxmock-backed                                                    |
| `*_integration_test.go` | Integration | Real Postgres, build tag `integration`                            |

Then:

1. Write the SQL migration: `migrations/NNNNN_*.sql` with `-- +goose Up` / `-- +goose Down` blocks.
2. Export `var RequiredTables = []string{"orders"}` from the feature's repository file and add `order.RequiredTables` to the `slices.Concat(...)` aggregation in [`cmd/api/main.go`](cmd/api/main.go). Startup refuses to boot if a required table is missing. (Schema verification is decoupled from `internal/db` — you never edit that core package.)
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

## Authentication

Authentication is **deliberately not implemented** in this template — the contract is. Any middleware that validates a token and sets the user ID in the request context will work.

### Contract

| Item             | Where                                                                   |
| ---------------- | ----------------------------------------------------------------------- |
| Context key      | `shared.WithUserID(ctx, id)` / `shared.UserIDFromContext(ctx)` (key lives in `internal/shared`) |
| User ID format   | **Must be a UUID.** `UserIDFromContext` canonicalises it and rejects non-UUIDs as 401 (see below). |
| Repository scope | Every query already has `WHERE … AND user_id = $N`                      |
| Wire point       | The `r.Route("/api/v1", …)` block in `cmd/api/main.go`                  |
| 401 response     | `shared.WriteUnauthorised(w, errorType, message)` (JSON envelope + `WWW-Authenticate`) |
| Metric on 401    | `api_errors_total{feature="log",error_type="unauthorised"}` (automatic) |

> **UUIDs only.** The `user_id` column is `UUID NOT NULL`, so `shared.UserIDFromContext` is the single enforcement point: it parses the stored value, returns the canonical lowercase UUID, and reports `ok=false` (→ 401) for anything that is not a UUID — keeping a non-UUID subject from reaching a query and surfacing as a confusing 500 (`error_type="database"`). If your IdP's `sub` is **not** a UUID (e.g. `auth0|12345`, an email, a numeric id), map it to an internal UUID in your middleware (a `sub → uuid` lookup table) **before** calling `shared.WithUserID`. `WithUserID` itself is a plain setter — validation lives only at the read point so it can't be bypassed by a second writer.

### Implementing

```go
r.Route("/api/v1", func(r chi.Router) {
    r.Use(authMiddleware.Handler) // ← your token validator

    r.Get("/logs", logHandler.ListLogs)
    // …
})

// Inside your middleware:
ctx := shared.WithUserID(r.Context(), userID)
next.ServeHTTP(w, r.WithContext(ctx))
```

Recommended libraries:

- [`coreos/go-oidc`](https://github.com/coreos/go-oidc) for OIDC + token verification
- [`golang.org/x/oauth2`](https://pkg.go.dev/golang.org/x/oauth2) for OAuth 2.0 clients

JWT claim checks (validate every one): `iss`, `aud`, `exp`, `nbf`, `kid`, and `alg` (server-side whitelist — never trust the token header). See the [OWASP JWT cheat sheet](https://cheatsheetseries.owasp.org/cheatsheets/JSON_Web_Token_for_Java_Cheat_Sheet.html).

---

## Database

[`internal/db`](internal/db/db.go) wraps `*pgxpool.Pool`. Repositories accept `db.PgxIface` — a three-method interface (`Exec`, `Query`, `QueryRow`) satisfied by both `*pgxpool.Pool` and `pgx.Tx`. The same repository code therefore runs inside and outside transactions, and is unit-testable with [pgxmock](https://github.com/pashagolub/pgxmock).

Native pgx auto-prepares and caches statements per connection — no manual `Prepare`/`Close` bookkeeping anywhere.

### Transactions

```go
err := database.WithTransaction(ctx, func(tx pgx.Tx) error {
    // every operation atomic
    return nil
})
```

### Schema verification at startup

`db.New()` checks that every table in the `requiredTables` slice it is passed exists. The slice is aggregated in `cmd/api/main.go` via `slices.Concat(userlog.RequiredTables, …)` — each feature exports its own `RequiredTables`, so `internal/db` is never edited when a feature is added. If any table is missing, the process exits with a clear "run `make db-migrate`" message.

---

## Pagination

`GET /api/v1/logs` supports two pagination modes. They are **mutually exclusive** — supplying both `cursor` and `offset` is a `400 invalid_pagination`.

### Which to use

| | **Cursor (keyset)** — recommended | **Offset** — legacy / shallow |
| --- | --- | --- |
| Query | `?cursor=` (first page), then echo `next_cursor` | `?limit&offset` |
| Response | `{"logs":[…],"next_cursor":"…"}` | bare array `[…]` |
| Cost | constant per page (index seek) | grows with depth (walk-and-discard) |
| Under concurrent inserts | stable — no shifted/duplicated rows | pages shift; rows can repeat or be skipped |
| Depth | unbounded | **hard cap at `offset` 10000** — deeper rows are unreachable |

**Use cursor pagination for anything that can exceed a few thousand rows or is read while being written.** Offset stays for shallow human browsing (jump to page N) and backwards compatibility, but its `10000` cap is a hard reachability limit, not a soft default — past it, switch to cursor.

### Cursor mechanics

- The first cursor page is requested with an empty value: `GET /api/v1/logs?cursor=&limit=50`. Each response carries `next_cursor`; pass it back as `?cursor=<token>` for the next page. `next_cursor` is **omitted on the last page**.
- The cursor is **opaque** — base64url of the last row's keyset `(date_and_time, id)`. Treat it as a token; do not construct or parse it client-side.
- Ordering is `date_and_time DESC, id DESC`. `id` is the tiebreaker so rows that share a timestamp paginate deterministically (the offset query uses the same order and the same `(user_id, date_and_time DESC, id DESC)` index — migration `00002`).
- A forged or tampered cursor **cannot cross user boundaries**: every query stays scoped by `user_id`. Cursor validation (strict RFC3339Nano + UUID parse, → `400` on anything malformed) exists to keep cast-error 500s and pathological values out of SQL, not for authorisation.

### Copying the pattern into a new feature

`internal/userlog` is the reference: the keyset SQL (`getLogsKeysetFirst` / `getLogsKeysetAfter` in `*_repository.go`), the opaque cursor codec (`*_cursor.go`), and the service's `getLogsCursor` (decode → query `limit+1` → emit `next_cursor`). Mirror those, and add the composite `(user_id, <sort_col> DESC, id DESC)` index in your migration.

---

## Migrations

The migrator is a separate binary at [`cmd/migrate`](cmd/migrate/main.go) (`go-api-migrator` — built by GoReleaser; `make prod-build` for a local snapshot). It embeds every `.sql` file under `migrations/` and applies them through goose's Provider API, serialised by a Postgres advisory lock.

### Local development

```bash
make db-migrate   # apply pending
make db-status    # show applied / pending
make db-reset     # roll back all  (dev-only — destructive gates inline)
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
| `down` / `down-to <v>` / `redo` / `reset` | Roll back — **destructive** |
| `status` / `db-version`                   | Read schema state           |

Destructive commands require BOTH gates:

```bash
MIGRATOR_ALLOW_DESTRUCTIVE=true go-api-migrator --confirm-destructive down
```

### Operational rules

1. **Expand / contract.** Every migration must be backwards-compatible with the currently-deployed API binary (rolling deploys run old code against new schema). Add columns as nullable or with defaults; drop columns only in a later release after no deployed binary reads them. Never rename in place — add, dual-write, drop later.
2. **Direct Postgres connection required.** The session locker uses `pg_advisory_lock`, which is per-session. PgBouncer transaction/statement pooling breaks this — use session pooling or connect directly.
3. **Tune timeouts.** `MIGRATOR_LOCK_TIMEOUT` (default `5s`) keeps a stalled lock acquire from blocking production writes. `MIGRATOR_STATEMENT_TIMEOUT` (default `10min`) — raise for migrations with data backfills; document the expected duration in the SQL file's header comment.
4. **Never edit an applied migration.** Once a migration has run against any shared/production database, fixes go in a new file. Editing an applied file silently de-syncs `goose_db_version` across environments.

---

## Observability

### Metrics

`/metrics` is served on the internal admin port (`:METRICS_PORT`, default 9090). The middleware records scrapes too — there is no recursion, the increment lands on the next scrape's snapshot.

The HTTP middleware is **panic-safe**: if a handler panics, a single deferred block still records the request (status `500` if no status was written, else the written status), restores the in-flight gauge, and re-panics so `chi.Recoverer` upstream still produces the 500 response.

#### HTTP

| Metric                          | Type      | Labels               |
| ------------------------------- | --------- | -------------------- |
| `http_requests_total`           | Counter   | method, path, status |
| `http_request_duration_seconds` | Histogram | method, path, status |
| `http_requests_in_flight`       | Gauge     | —                    |
| `http_response_size_bytes`      | Histogram | method, path, status |

`path` is the chi matched route pattern (`/logs/{id}`) for matched routes or the literal `unmatched` for 404s — bounded cardinality regardless of URL shape.

#### Database pool

Reported by a custom `prometheus.Collector` that reads `pool.Stat()` on every scrape (no background polling).

| Metric                                                                                     | Type    |
| ------------------------------------------------------------------------------------------ | ------- |
| `db_pool_acquired_conns`, `db_pool_idle_conns`, `db_pool_total_conns`, `db_pool_max_conns` | Gauge   |
| `db_pool_acquire_count_total`, `db_pool_acquire_duration_seconds_total`                    | Counter |
| `db_pool_empty_acquire_count_total`, `db_pool_canceled_acquire_count_total`                | Counter |

#### Database queries

Reported by a `pgx.QueryTracer` wired into the pool's `ConnConfig.Tracer`. Every query is instrumented automatically.

| Metric                      | Type      | Labels    |
| --------------------------- | --------- | --------- |
| `db_query_duration_seconds` | Histogram | operation |
| `db_query_errors_total`     | Counter   | operation |

`operation` is bounded to `SELECT`, `INSERT`, `UPDATE`, `DELETE`, `BEGIN`, `COMMIT`, `ROLLBACK`; anything else maps to `OTHER`. `pgx.ErrNoRows` is excluded from the error counter (it's a normal `QueryRow` outcome, not a database error).

#### API errors

| Metric             | Type    | Labels              |
| ------------------ | ------- | ------------------- |
| `api_errors_total` | Counter | feature, error_type |

Both labels come from package-level Go constants (`FeatureName` and the `ErrType*` constants in each feature's `errors.go`). `err.Error()` text is **never** used as a label value.

### Logs

Every line is JSON to stdout (no log files, no rotation in the app — the orchestrator does that). The shape is:

```json
{
  "time":"2026-06-07T15:30:00+10:00", "level":"INFO", "msg":"creating log entry",
  "service":"go-api", "version":"1.2.3", "commit":"abc1234",
  "request_id":"req-xyz",
  "method":"POST", "path":"/api/v1/logs", "ip":"203.0.113.5:1234",
  "user_id":"u-42"
}
```

| Field | Source | When present |
|---|---|---|
| `time` | slog default, formatted RFC3339 | every line |
| `level`, `msg` | slog default | every line |
| `service`, `version`, `commit` | `NewLogger(level, serviceName)` — bound once via `WithAttrs`; version/commit come from `internal/version` (`-ldflags`) | every line |
| `request_id`, `method`, `path`, `ip` | `RequestLogger` middleware via `WithRequestContext` | every line emitted during an HTTP request |
| `user_id` | re-bound by an auth middleware (recommended pattern) | every line emitted *after* auth fires |
| `error`, `actual_error` | only on `LogError` | error lines |
| `status`, `duration_ms` | only on the "request completed" line | end-of-request lines |

**One log line is enough to identify the request, the route, the client, and (after auth) the user.** A handler that returns an internal error logs:

```json
{"…", "level":"ERROR", "msg":"error occurred",
 "service":"go-api", "version":"1.2.3", "commit":"abc1234",
 "request_id":"req-xyz", "method":"GET", "path":"/api/v1/logs/{id}",
 "ip":"203.0.113.5:1234", "user_id":"u-42",
 "error":"database error",
 "actual_error":"getLog abc: database error: pq: connection refused"}
```

No correlation join needed — every field needed to triage the incident is on that single line.

**Wiring user_id (when you add auth):** in your auth middleware, after you've validated the token and pulled the user ID, re-wrap the request-scoped logger:

```go
log := logger.FromContext(r.Context(), defaultLogger)
log = log.WithRequestContext(logger.RequestContext{UserID: userID})
ctx = context.WithValue(r.Context(), logger.LoggerContextKey, log)
```

`WithRequestContext` only adds non-empty fields, so you don't lose the request_id/method/path/ip already bound — they stay; user_id joins them.

### Health endpoints

| Endpoint  | Purpose                                      | Use for                                    |
| --------- | -------------------------------------------- | ------------------------------------------ |
| `/livez`  | Process responsiveness; no dependency check  | k8s `livenessProbe`                        |
| `/readyz` | Dependency check (DB ping); 503 if unhealthy | k8s `readinessProbe`, load-balancer health |
| `/health` | Alias for `/readyz`                          | Backwards-compatibility                    |

`/readyz` and `/health` are mirrored on the internal admin port and, on the public listener, are gated by `PUBLIC_READINESS` (default `true`). The readiness result is cached for ~2s (concurrency-safe), and the cached DB ping runs on a context detached from the requesting client's cancellation — so a flood of probes collapses to at most one DB ping per window and a client aborting `/readyz` mid-ping cannot poison the shared result. Health endpoints (`/livez`, `/readyz`, `/health`) are **exempt** from the app-level rate limiter (a low `RATE_LIMIT_RPM` must never 429 liveness probes into a restart loop); they rely on the readiness cache and edge controls instead. Trade-off: exposing readiness publicly is convenient for external load-balancer health checks but discloses operational state and adds a (now-bounded) DB-ping vector — set `PUBLIC_READINESS=false` to keep it internal-only.

### What's NOT included

Prometheus, Grafana, and alertmanager are deliberately separate concerns — the application is a metrics _producer_; the observability stack should live in a separate infrastructure repo that scrapes this app. Dashboard JSON and Prometheus rules change for different reasons than business logic.

---

## HTTP security

| Header                    | Value                                        | Purpose                             |
| ------------------------- | -------------------------------------------- | ----------------------------------- |
| `Content-Security-Policy` | `default-src 'none'; frame-ancestors 'none'` | Strict CSP for a JSON API           |
| `X-Content-Type-Options`  | `nosniff`                                    | Prevent MIME sniffing               |
| `Cache-Control`           | `no-store`                                   | Don't cache authenticated responses |
| `Referrer-Policy`         | `no-referrer`                                | Prevent URL/token leakage           |

### CORS

Configured via `CORSConfig` in main.go. Origins from `CORS_ALLOWED_ORIGINS` (comma-separated, explicit origins only). The middleware matches origins exactly, so a wildcard entry is silently inert — **startup rejects any `*`** (and rejects `CORS_ALLOW_CREDENTIALS=true` with an empty origin list, which can never succeed). `Vary: Origin` is set on every response so shared caches don't poison. `Access-Control-Allow-Credentials: true` is emitted only when `CORS_ALLOW_CREDENTIALS=true`.

### Client IP (`X-Forwarded-For`)

`chimw.RealIP` resolves `X-Forwarded-For` / `X-Real-IP` onto `r.RemoteAddr` BEFORE the request logger, so the logged `ip` field is the resolved client IP, not the upstream proxy.

> **Trust assumption:** `X-Forwarded-For` is client-spoofable. `RealIP` is therefore **opt-in** via `TRUST_PROXY_HEADERS` (default `false`) and is only registered when the app sits behind a trusted reverse proxy or load balancer that overwrites these headers on every request. With the default `false` the headers are ignored and `r.RemoteAddr` is the real peer — safe for an app exposed directly to the internet. Leaving it off while spoofable headers are honoured would also poison any IP-keyed rate limiter (see below).

### Handled at the infrastructure layer

- **HSTS** — at the reverse proxy or load balancer (TLS terminates outside the app).
- **Rate limiting** — primarily at the proxy / API gateway / WAF (see [Deployment requirements](#deployment-requirements)). An opt-in app-level fallback exists (`RATE_LIMIT_RPM`) for edge-less deployments, disabled by default.

## Deployment requirements

This template expects two controls to live at the **network edge** (load balancer / API gateway / WAF), not in the application:

1. **TLS termination + HSTS.** TLS terminates outside the app; the app speaks plain HTTP behind the edge.
2. **Rate limiting.** The edge is the **primary** rate-limit control — it sees the true client IP, holds a cluster-wide view, and can shed load before it reaches the app.

The app ships an **opt-in fallback limiter** (`RATE_LIMIT_RPM`, default `0` = disabled, keyed by client IP) for deployments that genuinely have no edge limiter. Two caveats make it a fallback, not a replacement:

- **Per-instance counters.** `httprate` counts in memory per process, so with `N` replicas the effective global limit is `RATE_LIMIT_RPM × N`. A true global limit belongs at the edge.
- **IP keying needs trusted proxy headers.** If the app is behind a proxy but `TRUST_PROXY_HEADERS=false`, every request carries the proxy's IP, so all traffic shares one bucket and the limiter self-DoSes the API. The app logs a startup warning when `RATE_LIMIT_RPM > 0` while `TRUST_PROXY_HEADERS=false`.

The limiter applies only to business routes (`/api/v1/*` and any future routes added to that group); the health endpoints (`/livez`, `/readyz`, `/health`) are exempt so probes are never throttled into a restart loop. When the limiter fires it returns `429` with the standard JSON error envelope (`{"error":"rate_limited",…}`). In edge-limited deployments the `429` may instead come from the upstream gateway, whose body shape is its own.

**After auth lands:** re-key the limiter by user ID for business-aware, per-endpoint limits — the per-IP key is a coarse pre-auth stopgap.

---

## Coding rules

1. **Validate strictly, parameterise queries, encode on output.** The API stores exactly what the client sent. XSS prevention lives at the renderer; the API relies on `json.Encoder`'s HTML escaping (`<` → `<` etc.) plus parameterised pgx queries. The only input cleaning is `shared.SanitiseNullBytes` (Postgres `TEXT` cannot store `\x00`).
2. **Type and validate inputs.** UUID path params via `uuid.Parse` so a malformed id never reaches the database. Length limits via `rune_max` / `rune_min` / `rune_len` tags — rune count matches Postgres `VARCHAR(N)` semantics.
3. **Validate database output.** Every row read from Postgres is re-checked via `Model.Validate()` (defence in depth — a schema migration drift would surface here, not later in business logic).
4. **Authenticate by default.** Every API endpoint requires authentication unless explicitly excluded.
5. **Authorise in the repository.** Every query has `WHERE … AND user_id = $N`. No filtering in the handler that the database can do.
6. **Validate file uploads.** MIME type, file extension, size limit. None are implemented in the template — add them when you add an upload endpoint.

> Validate on input, parameterise on storage, encode on output — at each language boundary, using the tool that owns that boundary. Input sanitisation sits _below_ those three in OWASP's defence hierarchy ([Input Validation](https://cheatsheetseries.owasp.org/cheatsheets/Input_Validation_Cheat_Sheet.html) and [XSS Prevention](https://cheatsheetseries.owasp.org/cheatsheets/Cross_Site_Scripting_Prevention_Cheat_Sheet.html)): use it only when the destination language structurally cannot hold the input — this codebase's only case is `shared.SanitiseNullBytes`, because Postgres `TEXT` rejects `\x00`. Never use sanitisation as a substitute for validation, never to "neutralise" XSS (that belongs at the renderer, in the renderer's templating engine), and never as a "be safe" reflex on every string. Sanitisation silently mutates user data, gives false confidence in places where the actual defence is elsewhere, and when applied repeatedly across round-trips causes double-encoding bugs that are painful to debug.

---

## Scaling

This template follows the [12-factor](https://12factor.net/concurrency) process model: scale by running **more processes**, not by making one process bigger. The API is stateless, so horizontal scale is `N` identical instances behind a load balancer — no session affinity needed.

### Sizing the connection pool

`DB_MAX_OPEN_CONNS` is **per instance**, not cluster-wide. If you run 4 instances with `DB_MAX_OPEN_CONNS=25`, you have a 100-connection ceiling at Postgres. Set Postgres' `max_connections` accordingly (plus headroom for the migrator and any maintenance sessions).

`DB_MIN_CONNS` controls how many connections each instance eagerly warms up. With 4 instances at `DB_MIN_CONNS=10`, you have 40 idle connections waiting at all times. Tune low if you scale aggressively up and down; tune high if your traffic is steady and you want first-request latency to be low.

### When you need a worker process type

If you have **async work** — sending emails, processing webhooks, retrying failed external calls, batch jobs, scheduled tasks — do **not** run them inside the API process. Two reasons:

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

| Need | Use |
|---|---|
| Job queue with retries, scheduled, priority, idempotency | [`hibiken/asynq`](https://github.com/hibiken/asynq) (Redis-backed; production-ready; ships with a web UI) |
| Lightweight pub/sub or streams | [`redis/go-redis/v9`](https://github.com/redis/go-redis) directly with Redis Streams |
| Distributed cache / rate limiting | `redis/go-redis/v9` — no queue layer needed |

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

The queue client is injected into the handler the same way `HandlerMetrics` is — through the constructor, no globals.

#### 4. Add the worker binary

```
cmd/worker/main.go      # consumes jobs from Redis
internal/jobs/          # job handler functions, following the userlog layered pattern
```

`cmd/worker/main.go` mirrors `cmd/api/main.go`:

- Load config (same `config.Load()` — already reads `REDIS_URL`).
- Create the logger (`logger.NewLogger(cfg.Log.Level, "go-api-worker")` — note the distinct service name).
- Open the DB pool (same `db.New(...)`).
- Initialise the Prometheus metrics (same `metrics.New()`).
- Start an asynq.Server (or a Redis Streams consumer loop).
- Bind `/livez` + `/readyz` on `:METRICS_PORT` so k8s can probe it.
- Graceful shutdown on SIGINT/SIGTERM — drain in-flight jobs before exiting.

Job handler functions live in `internal/jobs/` and follow the same layered pattern as `internal/userlog/` — handler / service / repository. The handler in this case is the `asynq.HandlerFunc`, not an HTTP handler.

#### 5. Wire the worker into the release pipeline

Add a third build to `.goreleaser.yaml`:

```yaml
builds:
  - id: api         # existing
    main: ./cmd/api
    binary: go-api
    # … same ldflags
  - id: migrator    # existing
    main: ./cmd/migrate
    binary: go-api-migrator
    # … same ldflags
  - id: worker      # new
    main: ./cmd/worker
    binary: go-api-worker
    # … same ldflags
```

GoReleaser produces all three binaries on the same release tag. Deploy them together so version skew is impossible.

### Cache use cases (not queues)

If you need Redis for caching (session data, rate-limit counters, computed responses) rather than a queue:

- Use `redis/go-redis/v9` directly — no queue layer needed.
- Wrap Get/Set/Del in a `internal/cache/` package so feature code doesn't know it's Redis.
- **Do not** cache in process memory (`sync.Map`, package-level maps). That breaks the stateless-process rule and your cache is per-instance — useless for sharing rate limits.
- Set explicit TTLs on every key. Unbounded caches grow until Redis OOMs.

### What to delete from the template if you don't need Redis

Most APIs do not need a worker process to start. Don't add Redis pre-emptively. The right time to add it is the first time you write code that thinks "this should happen later" or "this should retry". Until then, the API + migrator combination is the right shape.

---

## Releases

Releases are cut by pushing a `v*` tag. [`.github/workflows/release.yml`](.github/workflows/release.yml) runs GoReleaser, which produces:

- Static binaries for linux/darwin/windows × amd64/arm64 (windows/arm64 skipped) — both `go-api` and `go-api-migrator`
- `checksums.txt` — SHA-256 over every binary
- `<binary>.sbom.json` — one SPDX-JSON SBOM per binary, generated by syft
- `multiple.intoto.jsonl` — SLSA Level 3 provenance, generated by the [`slsa-github-generator`](https://github.com/slsa-framework/slsa-github-generator) reusable workflow

### Release notes come from CHANGELOG.md

[`scripts/extract-changelog.sh`](scripts/extract-changelog.sh) extracts the `## [X.Y.Z]` section from [CHANGELOG.md](CHANGELOG.md) and passes it to GoReleaser via `--release-notes`. The workflow FAILS if the section is missing or empty — no tag gets released without a hand-written entry. **Commit messages do not feed the changelog.** There is no commit-format enforcement and no commit-derived changelog generation.

The trade-off you accept by choosing manual changelogs: every release costs a few minutes of writing; in exchange the changelog reflects what the binary user cares about, not what the commit graph happens to record.

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
| `pre-commit`        | Hooks bypassed locally with `--no-verify` — mandatory here. Pinned tool versions (golangci-lint, govulncheck, semgrep, pre-commit). |
| `test`              | `go build`, `go vet`, `go test -race`                                                                                               |
| `test-integration`  | Postgres 16 service container, `go run ./cmd/migrate up`, then `go test -race -tags integration ./...`                              |
| `goreleaser-config` | `goreleaser check` + `extract-changelog.sh` fixture tests — broken release config or script is caught on PR, not at tag time        |

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

This is a **request, not an enforced gate** — no hook or CI job rejects other formats, and history before this point predates the convention. The local gate is `make ci` (no podman) after every change and `make verify` (podman) before a commit; see the [Makefile reference](#makefile-reference).

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

| Command                               | Description                                           |
| ------------------------------------- | ----------------------------------------------------- |
| `make ci`                             | Umbrella gate (no podman): `go build` + `go vet` + `golangci-lint` + race unit tests |
| `make verify`                         | Full pre-commit gate (podman): `ci` + integration tests |
| `make test-unit`                      | Race-enabled unit tests (pgxmock — no real DB needed). `make test` is a deprecated alias. |
| `make test-pretty`                    | Table-formatted output over the same `-race` unit suite |
| `make test-integration`               | Real-Postgres integration tests (requires `make run`) |
| `make test-scripts`                   | Shell-script Go tests (extract-changelog fixture)     |
| `make lint` / `make fmt` / `make vet` | golangci-lint / gofmt / go vet                        |
| `make vulncheck` / `make semgrep`     | govulncheck / semgrep                                 |
| `make pre-commit-run`                 | Run every hook against every file                     |

### Release

There are no hand-rolled `build-binary` / `build-migrator` / `build-prod` targets. Release-shaped binaries come from a single source — GoReleaser — both locally (via the snapshot below) and in CI. `internal/version` is populated by GoReleaser's `-ldflags` only; the Makefile contains no `LDFLAGS` or version metadata, so the two paths can't drift.

| Command                              | Description                                                                                                                                             |
| ------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `make prod-build`                    | Local snapshot via GoReleaser — cross-compiled binaries in `dist/`, no SBOM (skip syft), no publish                                                     |
| `make goreleaser-check`              | Full release-pipeline pre-flight: config validate + extract-changelog tests + full snapshot + SLSA-subjects jq replay + native binary `--version` smoke |
| `make changelog-check VERSION=x.y.z` | Verify CHANGELOG has a non-empty section for the version (no leading `v`)                                                                               |

For a dev one-off binary just use `go build` or `go run` directly — those don't carry release ldflags and aren't meant to.

---

## Changelog

See [CHANGELOG.md](CHANGELOG.md). The format is [Keep a Changelog](https://keepachangelog.com). The release workflow extracts the relevant section automatically — a release fails if it's missing.
