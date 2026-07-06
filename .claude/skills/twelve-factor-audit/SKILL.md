---
name: twelve-factor-audit
description: Audit a Go codebase against the 12-Factor App methodology (12factor.net). Use when the user asks "is this 12-factor compliant", "audit against 12factor", "where am I doing 12 factor right/wrong", or wants a per-factor assessment of an API/service codebase. Walks all 12 factors, cites specific files and patterns as evidence, and produces a verdict table plus actionable improvements. Works on this repo (uses internal/userlog, cmd/migrate, GoReleaser as references) and on other Go services that follow similar patterns.
---

# /twelve-factor-audit — assess against 12factor.net

The 12-Factor App methodology ([12factor.net](https://12factor.net/)) is the canonical checklist for cloud-native service design. This skill walks all twelve factors, gathers evidence from the codebase, and produces a per-factor verdict.

The output is a **summary table** + **per-factor detail with citations** + **specific improvements**. Avoid hand-waving — every "strong" or "weak" verdict must cite a file path, a grep result, or a concrete pattern.

## Inputs

None required — the skill audits the current working directory. Optional clarifying questions if patterns are ambiguous (e.g. "is there a worker process binary?", "where do you put async work?").

## Verdict scale

- **Strong** — implements the factor's strict reading; would pass a 12-factor purist's audit
- **Medium-strong** — foundation correct but missing a recommended extension (e.g. has stateless processes but doesn't ship multiple process types)
- **Medium** — partially follows the factor; one or two clear gaps
- **Weak** — significant deviation from the factor's intent
- **Violates** — actively does what the factor says not to

Always cite evidence. If a factor doesn't apply (rare for service codebases), say so explicitly and explain why.

## Checks by factor

### 1. Codebase

Required: one git repo, many deploys from the same tree.

| Evidence to gather | How |
|---|---|
| Is this a single git repo? | `git rev-parse --show-toplevel` returns one dir |
| Is the same tree the source of all deploys? | Look for `.goreleaser.yaml` / a CI release workflow that produces deployable artifacts from the repo |
| Are there forked deployment branches? | `git branch -r` — many long-lived deploy branches is a smell |

### 2. Dependencies

Required: explicit declaration, no implicit system deps at runtime.

| Evidence | How |
|---|---|
| `go.mod` + `go.sum` declared | `test -f go.mod && go mod verify` |
| `CGO_ENABLED=0` in production builds | `grep -E 'CGO_ENABLED=0' .goreleaser.yaml container.prod Makefile` |
| Static binaries | Check `.goreleaser.yaml` for cgo + ldflags settings |
| Tool versions pinned | `grep -rE '@latest' container.* .github/workflows/*.yml` — should return nothing |
| SBOM generation | `grep -A3 'sboms:' .goreleaser.yaml` |
| Vulnerability scanning | `grep govulncheck .pre-commit-config.yaml .github/workflows/*.yml` |
| Distroless / minimal prod image | `grep -E 'distroless|scratch' container.prod` |

### 3. Config

Required: config from environment, not from code.

| Evidence | How |
|---|---|
| Single config package | `find . -path './internal/config*' -name '*.go'` |
| No scattered env reads | `grep -rn 'os.Getenv' --include='*.go' \| grep -v internal/config \| grep -v _test.go` — should return nothing (or only documented bootstrap exceptions like a healthcheck CLI flag) |
| Typed Config struct | `grep -E 'type Config struct' internal/config/*.go` |
| Validation on startup | Search for the config-validation function and confirm it fails fast on missing required vars |
| Malformed numerics fail fast | Search for a `getEnvInt` or similar that returns an error, not a silent default |

If any `os.Getenv` is in `cmd/` or `internal/`, flag the exception — it must be documented as a bootstrap necessity (e.g. a probe CLI flag) or it's a violation.

### 4. Backing services

Required: every dependency is an attached resource referenced by config — swappable without code change.

| Evidence | How |
|---|---|
| DB referenced by env vars only | `grep -rn 'localhost\|127.0.0.1' --include='*.go' \| grep -v _test.go \| grep -v cli.go` — should be empty (or only the healthcheck probe target) |
| Connection details from config | `grep 'DB_HOST\|DB_PORT' internal/config/config.go` |
| Same DB driver for dev and prod | Compare compose Postgres version to CI Postgres service to prod connection settings |
| No hardcoded URLs/hosts | Grep for production hostnames, S3 buckets, queue endpoints in source |

If a Redis, queue, or cache backing service is present (see CLAUDE.md or `cmd/`), verify it follows the same env-driven pattern.

### 5. Build, release, run

Required: build is reproducible from a commit; release combines build + config; run is just executing the release.

| Evidence | How |
|---|---|
| Reproducible builds | `-trimpath` flag present in GoReleaser config or build commands |
| Version baked at build time | `grep -E '\-X .+\.Version=' .goreleaser.yaml` |
| Static binaries | `grep -E 'CGO_ENABLED=0' .goreleaser.yaml` |
| Immutable releases | Release artifacts include checksums, ideally SLSA provenance |
| No config baked into binaries | Confirm `internal/config` reads env at startup, not from compiled-in defaults that can't be overridden |
| Single source of truth for ldflags | Check Makefile — if it has its own `LDFLAGS := -X …version.Version=…` AND `.goreleaser.yaml` does too, that's drift waiting to happen |

### 6. Processes

Required: stateless, share-nothing processes. Persistent state in a backing service.

| Evidence | How |
|---|---|
| No in-memory sessions | `grep -rn 'sync.Map\|sessions\[' --include='*.go'` — looking for session-cache patterns |
| Request-scoped state on context | `grep -rn 'context.WithValue' --include='*.go'` — should be the pattern |
| No global mutable state | Look for package-level vars that aren't constants, registries, or once-initialised pools |
| Auth via context, not memory | Search for how the user ID flows — should be via `context.Context`, not a process-wide map |

### 7. Port binding

Required: app self-contains the HTTP server; no external nginx/Apache required.

| Evidence | How |
|---|---|
| Uses `net/http` directly | `grep -rn 'http.Server\|ListenAndServe' --include='*.go' cmd/` |
| No reverse-proxy assumption in code | The app should run standalone; if it requires an external server to function, that's a violation |
| Listens on env-configured port | `grep PORT internal/config/config.go` |
| Multi-listener split is OK | A public port + internal admin port is good (`:PORT` + `:METRICS_PORT`) — both are app-managed |

### 8. Concurrency

Required: scale by running more processes, not by making one process bigger.

| Evidence | How |
|---|---|
| Stateless processes (covered by factor 6) | Allows horizontal scale |
| Multiple process types | `ls cmd/` — `api`, `migrator`, `worker`, `scheduler`, etc. Each `cmd/<x>` is one process type |
| Pool sizing is per-instance | Check that `DB_MAX_OPEN_CONNS` etc. are documented as per-instance, not cluster-wide |
| Documented scaling story | README should mention horizontal scale and the per-instance math (`DB_MAX_OPEN_CONNS × N instances ≤ Postgres max_connections`) |

If the codebase has async work (jobs, scheduled tasks, webhooks) but everything is in the API process — that's a Medium or Weak verdict. Recommend extracting into a separate `cmd/worker/main.go`.

### 9. Disposability

Required: fast startup, graceful shutdown, robust against forced termination.

| Evidence | How |
|---|---|
| Signal handling | `grep -rn 'signal.Notify\|signal.NotifyContext' --include='*.go' cmd/` |
| Graceful HTTP shutdown | `grep -rn 'Shutdown(ctx' --include='*.go' cmd/` |
| DB pool closed in shutdown | Look for `pool.Close()` in the shutdown path |
| Startup is fast | Schema verification should be a quick check, not a heavy migration. Migrations belong in a separate process (factor 12) |
| No `os.Exit` in server goroutine | Server-start errors should flow through graceful cleanup, not bypass it |
| Crash-safe state | Database transactions, idempotent writes, no append-then-rename patterns that leave half-written files |

### 10. Dev/prod parity

Required: same backing services, same languages, same code paths in dev and prod.

| Evidence | How |
|---|---|
| Same DB version dev/prod/CI | Compare `compose.dev.yaml`, `.github/workflows/ci.yml`, and any prod docs |
| Same migrations | Both use the same `migrations/` directory + same migrator binary |
| Same env var names | Dev `.env.example`, CI workflow env, prod docs — all use the same names |
| No dev-only code paths | `grep -rn 'if env == "dev"' --include='*.go'` should be empty |

The only acceptable drift is TLS mode (`DB_SSLMODE=disable` in dev, `require` in prod) because compose-Postgres TLS is unusual.

### 11. Logs

Required: write to stdout as event stream; the environment handles routing, rotation, and aggregation.

| Evidence | How |
|---|---|
| Logs go to stdout | `grep -rn 'os.Stdout\|os.Stderr' --include='*.go' internal/shared/logger/` |
| Structured (JSON) | `grep -rn 'JSONHandler\|slog.NewJSON' --include='*.go'` |
| No log files | `grep -rn 'OpenFile\|os.Create.*\.log' --include='*.go'` should be empty |
| No log rotation in app | `grep -rn 'lumberjack\|rotation' --include='*.go'` should be empty (use the orchestrator's rotation) |
| Service identity on every line | Look for `service`, `version`, `commit` attrs bound onto the logger handler — needed for rolling-deploy correlation |

### 12. Admin processes

Required: one-off admin tasks (migrations, data fixes) are separate processes from the same codebase, sharing config.

| Evidence | How |
|---|---|
| Migrator is its own binary | `ls cmd/migrate` should exist |
| Same codebase, same config | The admin binary uses `internal/config` + `internal/version` — not a separate repo |
| Same release pipeline | `grep -A5 'builds:' .goreleaser.yaml` shows multiple build IDs (api + migrator + …) |
| Run on demand | Migrator runs before deploy, not inside the API process |
| Documented operational pattern | README should say "run migrator before deploying API" |

## Output format

Produce **two sections**:

### 1. Summary table

```
| # | Factor              | Verdict       |
|---|---------------------|---------------|
| 1 | Codebase            | Strong        |
| 2 | Dependencies        | Strong        |
| ...
```

### 2. Per-factor detail

For each factor, a short paragraph:
- What you found (cite file paths + grep results)
- Why it's strong/medium/weak (the specific 12-factor criterion)
- (If not strong) what would close the gap, concretely

Length target: ~80-120 words per factor for the detail section; tighter for "Strong" verdicts, longer for "Medium" or worse.

End with a `TL;DR` count: how many strong, how many medium, how many weak/violating. Note any deliberate trade-offs the codebase has documented (e.g. "no async worker process type — the template is a single web process, added only when a real need exists — out of scope for an API-template assessment").

## Common findings on this template

For reference, when applied to a recent state of this repo:

- **Strong** on 11/12.
- **Medium-strong** on factor 8 — the template ships `cmd/api` + `cmd/migrate` only. If async work exists, a third process type is recommended (see the README's "Scaling" section for the Redis-backed worker pattern).
- One documented exception to factor 3: `cmd/api/cli.go` has a `--port` flag default (8080) used in `--healthcheck` mode. The Dockerfile passes `--port` explicitly so there's one source of truth.

When auditing a fork of this template, those baselines should hold. The interesting findings are deviations from the baseline.

## Non-negotiables

- **Cite evidence.** Every verdict must have at least one file path, grep result, or pattern citation. Vibes are not evidence.
- **Distinguish "not implemented" from "wrong".** Async workers, file uploads, and many other features may be deliberately absent in a template. That's not a 12-factor violation — flag it as scope-not-implemented.
- **Don't recommend over-engineering.** If a codebase has no async work, don't recommend adding a worker process type just to "improve" factor 8. The factor is about the right pattern when the need exists, not about always having one of everything.
