# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Release notes for tagged versions are extracted from this file automatically by [`scripts/extract-changelog.sh`](scripts/extract-changelog.sh) and passed to GoReleaser via `--release-notes`. **Commit messages do not feed the changelog** — every release requires a hand-written `## [X.Y.Z]` section here, or the release workflow fails.

> **Changelog reset — 2026-06-13.** This changelog was reset and versioning restarted at **0.1.0** because the project has been repositioned as a **template for LLM-assisted development of secure, efficient Go REST APIs**. The previous entries recorded the template's *construction* — incremental hardening of a codebase being built — rather than *released* versions of a product. They were removed so the changelog describes the template as it stands today, not the journey of building it. No version was ever published from those entries; an earlier scaffold-phase `v1.0.0` tag (2026-04-05) predates this repositioning and was never released or consumed, so reusing `0.1.0` for the first real release is deliberate. That tag is the repository owner's to reconcile.

## [Unreleased]

The 0.1.0 baseline — a production-ready Go REST API template, designed so that human- and LLM-assisted development can extend it without re-deriving its conventions. Grouped by theme rather than chronology.

### Architecture

- Layered feature packages: **handler → service → repository**, one package per feature, **no cross-feature imports** (shared concerns in `internal/shared/`). [`internal/userlog/`](internal/userlog/) is the canonical, copyable reference.
- Features self-register: each exposes `Routes()` and `RequiredTables`, aggregated in [`cmd/api/main.go`](cmd/api/main.go) so the core never edits when a feature is added.
- Transaction control lives in the service: multi-statement operations use the `db.WithTransaction` pattern with a tx-scoped repository; single statements are never wrapped.

### API surface

- CRUD plus **atomic batch create** on `/api/v1/logs` (any failure rolls back the batch), and two pagination modes — offset and opaque keyset **cursor** — with the documented bare-array vs `CursorPage` response shapes.
- A **single JSON error envelope** (`{"error":"<bounded_type>","message":…}`) on every response including router-level 404/405; error types are bounded constants, never lower-layer text.
- An OpenAPI spec kept **in lockstep with the code by contract tests** (documented pagination defaults/maxima, batch cap, and `log` length all assert against the Go constants); strict JSON decoding rejects unknown fields and trailing data; oversized bodies return 413.

### Security

- The **seven coding rules** (validate / parameterise / encode) in [`.claude/rules/security.md`](.claude/rules/security.md), with `internal/userlog/` as their reference implementation.
- **UUID user-ID identity boundary** enforced at a single read point (`shared.UserIDFromContext`); `WithUserID` is the auth seam adopters wire to their IdP (auth is intentionally contract-only).
- Repository-level ownership scoping (`WHERE … AND user_id = $N`), opt-in IP rate limiting (`RATE_LIMIT_RPM`, edge-first), opt-in proxy-header trust (`TRUST_PROXY_HEADERS`), CORS validated at startup, destructive-migration gates, and `updated_at` owned by a trigger.
- A scanner suite: **gitleaks (now scanning `.claude/`)**, golangci-lint incl. gosec, govulncheck, semgrep, plus `make deadcode` for whole-program reachability.

### Observability

- **Bounded-label** Prometheus metrics (`http_requests_total`, `http_request_duration_seconds`, `db_query_duration_seconds`, `api_errors_total`), a pgx **pool collector**, and a query tracer.
- Structured `slog` logging with service identity and per-request context bound onto every line, including a real **`LogWarn`** level (so `quiet` shows warnings, not just errors).
- Health endpoints — `/livez` always public, `/readyz` + `/health` gated by `PUBLIC_READINESS` — backed by a concurrency-safe **~2s readiness cache** (ping detached from caller cancellation; `context.Canceled` never cached).

### Tooling

- A Makefile taxonomy where each command lives in exactly one target and the umbrellas **`ci`** (no podman) and **`verify`** (podman) *compose* the granular targets; plus `deadcode` and the granular test/quality targets.
- A dev compose stack (`compose.dev.yaml`) that passes the whole `.env` to the app container with port mappings tracking `$PORT`/`$METRICS_PORT`.
- A **GoReleaser** release pipeline: cross-compiled binaries, SPDX SBOMs, checksums, and **SLSA Level 3** provenance, cut by pushing a `v*` tag.

### Agent workflow

- [`CLAUDE.md`](CLAUDE.md) carries the verification loop (`make ci` / `make verify`, podman vs no-podman) and a Conventional-Commits convention (requested, not enforced; no `Co-Authored-By` attribution).
- [`.claude/rules/security.md`](.claude/rules/security.md) and [`.claude/rules/decisions.md`](.claude/rules/decisions.md) record the non-negotiables and the settled trade-offs; review skills (`/security-review`, `/architecture-review`, `/performance-review`) plus `/new-feature` and `/twelve-factor-audit` operationalise them.

<!-- On tagging the first release, retitle the section above from `## [Unreleased]` to `## [0.1.0] - <date>` (the release workflow's scripts/extract-changelog.sh extracts the matching `## [X.Y.Z]` section). Tagging `v0.1.0` is the repository owner's act. -->
