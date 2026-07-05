# go-api-template

Production-ready Go API template. Layered architecture (handler → service → repository), one package per feature. Native pgx v5, embedded migrator, OpenTelemetry-instrumented (traces and metrics over OTLP to a Collector), GoReleaser pipeline with SLSA L3.

## Common commands

| Command | Use for |
|---|---|
| `make ci` | **Single verification loop** — build + vet + lint + race unit tests. Run after every change. |
| `make run` | Start the dev stack (Postgres + app, hot reload via Air) |
| `make test-unit` | Race-enabled unit tests (pgxmock, no real DB). `make test` is a deprecated alias. |
| `make verify` | Full pre-commit gate (podman): `ci` + integration tests. |
| `make test-integration` | Real-Postgres integration tests (run `make run` first) |
| `make lint` | `golangci-lint` — must be `0 issues` before commit |
| `make db-migrate` | Apply pending migrations via the embedded migrator |
| `make db-status` | Show applied / pending migrations |
| `make pre-commit-run` | Run every pre-commit hook against every file |
| `make changelog-check VERSION=x.y.z` | Verify CHANGELOG section exists before tagging |

## Project conventions

- **Reference feature package:** [`internal/userlog/`](internal/userlog/) — the canonical pattern. Copy it for every new feature. Named `userlog` (not `log`) to avoid shadowing the stdlib. When you copy, pick another stdlib-safe name. **No cross-feature imports** — shared concerns go in [`internal/shared/`](internal/shared/).
- **Env vars:** never call `os.Getenv` outside [`internal/config`](internal/config/config.go). Add new vars to the typed `Config`; the strict-int parsing and startup validation are already wired.
- **String length:** use the custom `rune_max` / `rune_min` / `rune_len` validator tags registered via `shared.RegisterRuneLenValidators` — **never** the stdlib `max` / `min` / `len`. Rune count matches Postgres `VARCHAR(N)` semantics.
- **Errors:** repositories wrap with multi-`%w` (sentinel + underlying driver error). Handlers map every error in `handleError` and emit `api_errors_total{feature,error_type}` — both labels are bounded constants, never derived from `err.Error()`.
- **Two listeners:** `:PORT` is public and `:METRICS_PORT` is internal admin (mirrored `/livez`, `/readyz`, `/health`). The app serves no `/metrics` scrape endpoint. Metrics are pushed via OTLP to a Collector. The internal port has no auth, so restrict at the infra layer.
- **Rate limiting:** expected at the network edge (LB / gateway / WAF) — that is the primary control. The app-level limiter (`RATE_LIMIT_RPM`, default `0` = disabled, keyed by IP) is only a fallback for edge-less deployments; its counters are per-instance. Post-auth, re-key it by user ID for business-aware per-endpoint limits. See README "Deployment requirements".
- **Proxy trust:** `chimw.RealIP` is opt-in via `TRUST_PROXY_HEADERS` (default `false`) — `X-Forwarded-For`/`X-Real-IP` are spoofable, so trust them only behind a proxy that overwrites them.
- **Errors over the wire:** every error response from a handler or the public router (incl. 404/405) is the JSON envelope `{"error":"<bounded_type>","message":…}` written via `shared.WriteJSONError` / `WriteJSONErrorPayload` — never `http.Error`. The only body-less exceptions are chi's `Timeout` 504 and `Recoverer` panic-500 (those middlewares own their responses). Router-level types (`not_found`, `method_not_allowed`) live in `internal/shared` and are not on `api_errors_total`. Oversized bodies are `413` (`body_too_large`); strict JSON decoding rejects unknown fields and trailing data. Pagination default is `shared.DefaultPageSize` (100), single-sourced with the OpenAPI spec; the log length limit lives only in the service (`shared.LogMaxChars`), not in struct tags.
- **Readiness:** `/readyz` + `/health` are cached ~2s (concurrency-safe; the cached ping runs on a `context.WithoutCancel` context so a disconnecting client can't poison the shared result) and gated on the public listener by `PUBLIC_READINESS` (default `true`); they are always mirrored on `:METRICS_PORT`. `/livez` is always public. Health endpoints are exempt from the app-level rate limiter (it applies only to the `/api/v1` business-route group).
- **Logging:** default `LOG_LEVEL` is `production` (fail-safe). Constructors never mutate global state — `slog.SetDefault` is installed once from `main` via `logger.SetDefaultSlog`. Only the handler takes a logger; repository/service do not.
- **Migrations:** edits to applied SQL files are forbidden. Fixes go in a new file. The trigger `update_updated_at_column` is the single source of truth for `updated_at` — never `SET updated_at = NOW()` in UPDATE statements.
- **Comments are load-bearing context** — see [`/write-comments`](.claude/skills/write-comments/SKILL.md). Prefer why-not-what, record evidence (e.g. the `EXPLAIN` plan comment), cite `decisions.md` where a comment defends a trade-off, mark deliberately-uncalled code with `Template surface:`, and write TODOs as `TODO(scope): … — <pointer>` (a pointer-less TODO is a finding).
- **Same-change rule:** any behaviour change updates every comment, doc, and spec that describes it **in the same commit** — a stale comment is a bug, not a cleanup for later.

## Always-apply rules

**Never commit — the owner commits, agents never do.** Agents MUST NOT run `git commit`, `git tag`, or `git push`. This is non-negotiable and is not waived by any task instruction. Leave every change uncommitted in the working tree for the repository owner to review and commit. In place of committing, **propose a [Conventional Commit](#commit-messages) message per logical change in your report** (and, when several logical changes are in flight, one per change). Adding `Bash(git commit*)` / `Bash(git tag*)` / `Bash(git push*)` to the deny list in [.claude/settings.json](.claude/settings.json) makes this mechanical as well as documented — the repository owner applies that (settings.json is deny-protected from agent edits by design).

**Verification loop:** after every change, run the no-podman gate `make ci` (build + vet + lint + race unit tests) and fix failures before moving on. `make lint` must report `0 issues`. Before finishing (so the owner can review and commit a green tree), run the full gate `make verify` (`ci` + integration tests) when podman is available. When podman is **not** available, run `make ci` only and say so explicitly in your report — state that integration tests were not run; never imply they passed.

- **Need podman:** `make verify`, `make test-integration`, the `db-*` targets (`db-migrate`/`db-status`/`db-reset`/`db-wait`), and `make run` / `make build`.
- **Never need podman:** `make ci`, `make test-unit`, `make test-scripts`, `make lint`, `make vet`, `make vulncheck`, `make semgrep`, `make deadcode`.

`make deadcode` (whole-program dead-code reachability) is an **occasional** check, not per-change. Its only expected hits are deliberate template surface (`internal/httpclient`, `shared.WithUserID`), each carrying a `Template surface:` doc marker.

**Agent permissions:** the allow-list in [.claude/settings.json](.claude/settings.json) holds only durable, generic, non-destructive commands (e.g. `make *`, `go test *`, `go vet *`, `go run *`, `golangci-lint run *`, `gofmt -w *`). One-off approvals — specific `sed`/`perl`/`gofmt <file>` invocations, `git checkout` (can discard uncommitted work), `go get` (a supply-chain decision), `export`/`podman`/`curl` one-liners — must NOT be persisted there; approve them per session instead.

**Security:** read [.claude/rules/security.md](.claude/rules/security.md) before writing any handler, repository, or migration. Non-negotiable.

**Settled decisions:** read [.claude/rules/decisions.md](.claude/rules/decisions.md) before "improving" anything adjacent — it records deliberate trade-offs. Do not relitigate them unless the user explicitly asks.

## Commit messages

Agents never commit (see the never-commit rule above); this convention is what the repository **owner** follows, and what agents use when **proposing** a commit message in their report — never to run `git commit` themselves.

Commits **should** follow [Conventional Commits v1.0.0](https://www.conventionalcommits.org/en/v1.0.0/): `type(scope): summary`. Types in use here: `feat`, `fix`, `build`, `ci`, `docs`, `test`, `perf`, `refactor`, `chore`. Repo-flavoured examples:

- `feat(userlog): add atomic batch create endpoint`
- `fix(db): bound pool sizes before the int32 conversion`
- `build(make): split test targets from the ci umbrella`
- `docs: document the verification loop`
- `test: cover LogWarn level gating`

Breaking changes use `!` after the type/scope **and** a `BREAKING CHANGE:` footer (e.g. the module rename would have been `refactor!:`). This is **requested, not enforced** — no hook or CI gate rejects other formats, and agents must not add one.

**Attribution:** commits must **not** carry `Co-Authored-By: Claude …` trailers or "Generated with Claude Code" footers — authorship stays with the repository owner. This is enforced for the trailer via `"attribution": { "commit": "", "pr": "" }` in [.claude/settings.json](.claude/settings.json) (empty strings hide all commit/PR attribution); the footer is governed by this rule.

## Skills

- `/new-feature` — scaffold a new feature package end-to-end (copy + rename + migration + feature `RequiredTables` + main.go wiring + `Routes` + OpenAPI + CHANGELOG entry). Definition at [.claude/skills/new-feature/SKILL.md](.claude/skills/new-feature/SKILL.md).
- `/security-review` — review a diff/package against **OWASP ASVS 5.0.0 (Level 2)** via the committed applicability map, with the seven security rules as the write-time rule set and the scanner suite. Definition at [.claude/skills/security-review/SKILL.md](.claude/skills/security-review/SKILL.md).
- `/architecture-review` — check layering, pattern conformance, and single-source-of-truth against [decisions.md](.claude/rules/decisions.md). Definition at [.claude/skills/architecture-review/SKILL.md](.claude/skills/architecture-review/SKILL.md).
- `/performance-review` — measure-before-changing review of queries, bounded work, and hot paths. Definition at [.claude/skills/performance-review/SKILL.md](.claude/skills/performance-review/SKILL.md).
- `/twelve-factor-audit` — audit this codebase (or any Go service following similar patterns) against the [12-factor](https://12factor.net/) methodology with file-cited evidence per factor. Definition at [.claude/skills/twelve-factor-audit/SKILL.md](.claude/skills/twelve-factor-audit/SKILL.md).
- `/write-unit-tests` — write tests matching the repo's discipline: table-driven subtests with useful got/want failures, behaviour-over-implementation assertions, the layer rule (pgxmock / interface fakes / real-router 413 test), determinism (`-race`), and a tripwire proof per guarded invariant. No coverage-percentage gate. Definition at [.claude/skills/write-unit-tests/SKILL.md](.claude/skills/write-unit-tests/SKILL.md).
- `/write-comments` — comments as load-bearing context (stale comments are bugs): why-not-what, evidence comments, `decisions.md` citations, `Template surface:` markers, the same-change rule, and the `TODO(scope): … — <pointer>` policy. Definition at [.claude/skills/write-comments/SKILL.md](.claude/skills/write-comments/SKILL.md).
- `/write-readme` — write README top sections a newcomer can follow: plain language, orientation-not-reference structure, and checkable rules (≤3-sentence intro, copy-paste-verified quickstart, every acronym defined, read-aloud pass). Definition at [.claude/skills/write-readme/SKILL.md](.claude/skills/write-readme/SKILL.md).

## Changelog discipline

Release notes are extracted from `CHANGELOG.md` by `scripts/extract-changelog.sh`. The release workflow **fails** if the `## [X.Y.Z]` section is missing or empty. Add entries to `## [Unreleased]` as you ship changes — commit messages do not feed the changelog.
