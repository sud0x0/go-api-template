# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Release notes for tagged versions are extracted from this file automatically by [`scripts/extract-changelog.sh`](scripts/extract-changelog.sh) and passed to GoReleaser via `--release-notes`. **Commit messages do not feed the changelog** — every release requires a hand-written `## [X.Y.Z]` section here, or the release workflow fails.

## [Unreleased]

### Added

- Two-layer authentication and authorisation, wired at the `/api/v1` seam and **disabled by default** so local dev needs no IdP.
  - **Layer 1 — authentication (OIDC).** `internal/middleware/auth_middleware.go` validates a Bearer access token against the IdP's discovered JWKS via `coreos/go-oidc` (signature, algorithm allowlist RS256/ES256 with no `none`, `exp`/`nbf`, audience, exact issuer match). It maps `iss|sub` to an internal UUID (`UUIDv5`) and stores it via `shared.WithUserID`.
  - **Layer 2 — authorisation (OPA).** `internal/middleware/authz_middleware.go` asks an OPA sidecar over its REST Data API `{subject, roles, action, resource, method}` and enforces the decision **fail-closed** (any error or non-`true` result denies with 403). Row-ownership `WHERE user_id = $N` SQL scoping stays as a separate layer.
  - New config: `OIDC_ISSUER_URL`, `OIDC_AUDIENCE`, `OIDC_ENABLED`, `OPA_URL`, `OPA_DECISION_PATH`, `OPA_TIMEOUT_MS`, `OPA_ENABLED` (see `.env.example`), validated fail-fast in `internal/config`.
  - Starter Rego policy and tests under `deploy/opa/`, and an `opa` sidecar service in `compose.dev.yaml`.
  - `shared.ErrTypeUnauthorised` (401) and `shared.ErrTypeForbidden` (403) router-level error types.

### Changed

- `decisions.md` #13 rewritten (auth is now implemented as a resource-server model) with new entries #16–#19; the ASVS map expands V9 and V10.3/V10.5 to per-requirement `met` rows and marks the authorization-server / login-flow requirements `n/a`.

<!-- Add entries here as you ship changes. Keep a Changelog groups: Added, Changed, Deprecated, Removed, Fixed, Security. On the first release, retitle this section to `## [X.Y.Z] - <date>` — scripts/extract-changelog.sh extracts the matching `## [X.Y.Z]` section, and tagging is the repository owner's act. -->
