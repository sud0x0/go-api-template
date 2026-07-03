---
name: security-review
description: Security-review a change, PR, or feature package in this Go API template. Use when the user says "security review this change/PR/feature", "is this secure", "audit the security of X", "check this for vulnerabilities", or asks whether new code respects the repo's security rules. Walks the seven rules in .claude/rules/security.md one by one with file-cited verdicts, runs template-specific checks (user_id scoping, bounded error labels, strict decoding, the UUID identity boundary, config-only env reads, migration gates, dependency justification), and runs the scanner suite. Produces a verdict table + findings with citations + the commands actually run.
---

# /security-review — review a change against the seven rules

This codebase's security model is written down in [`.claude/rules/security.md`](../../rules/security.md) (the source of truth) and demonstrated in [`internal/userlog/`](../../../internal/userlog/) (the reference implementation of all seven rules). This skill takes a diff or a package and checks it against that model, then runs the scanners. **Cite evidence for every verdict** — a file:line, a grep result, or a scanner line. "Looks fine" is not a verdict.

## Inputs

- **Scope** — the diff (`git diff main...HEAD`), a named package (`internal/<feature>/`), or the working tree. If unstated, default to the branch diff vs `main` and say so.
- Read [`.claude/rules/decisions.md`](../../rules/decisions.md) first: a finding that contradicts a settled decision is noted as *intentional*, not raised as a bug.

## Verdict scale

- **Pass** — rule satisfied, with a citation.
- **Pass (template seam)** — not implemented because the template ships only the contract (e.g. auth); the seam is correct.
- **Gap** — rule not satisfied; must fix before merge. Cite the offending line.
- **N/A** — the change doesn't touch this rule's surface (say why).

## Part 1 — the seven rules

Walk each rule in [`security.md`](../../rules/security.md) against the scope:

| # | Rule | What to check | How |
|---|---|---|---|
| 1 | Validate / parameterise / encode | No string-built SQL; output via `json.Encoder` (HTML-escaping on); the ONLY input cleaning is `shared.SanitiseNullBytes`. | `grep -rn 'fmt.Sprintf.*SELECT\|+ "SELECT\|Query(.*+' <scope>` → nothing; `grep -rn 'Sanitise' <scope>` |
| 2 | Type & validate every input | UUID path params via `uuid.Parse` (400 not 500); lengths via `rune_max`/`rune_min`/`rune_len`, never stdlib `max`/`min`/`len`. | `grep -rn 'uuid.Parse\|rune_max\|rune_min\|rune_len' <scope>`; `grep -rn '`json[^`]*` *validate:"[^"]*\b(max|min|len)=' <scope>` → nothing |
| 3 | Validate DB output too | Every row read re-checked via `Model.Validate()`. | `grep -rn '\.Validate()' <scope>` in repository read paths |
| 4 | Authenticate by default | New endpoints sit under the authenticated `/api/v1` group; user ID read via `shared.UserIDFromContext` (UUID-enforced), set via `shared.WithUserID`. | `grep -rn 'UserIDFromContext\|WithUserID' <scope>`; confirm routes are inside the `r.Route("/api/v1", …)` block |
| 5 | Authorise in the repository | Every query carries `WHERE … AND user_id = $N`; no row filtering in the handler. | `grep -rn 'user_id = \$' <scope>` — one per query; read the WHERE clauses |
| 6 | Validate file uploads | If an upload endpoint is added: MIME + extension + size, all three. | only if the scope adds uploads |
| 7 | Errors carry causes; labels bounded | Repos wrap multi-`%w` (sentinel + driver err); every metric/`error_type`/`path` label is a Go constant / chi pattern / fixed allow-list — never `err.Error()`, raw SQL, or user strings. | `grep -rn 'IncAPIError\|ErrType\|%w: %w' <scope>`; confirm labels are constants |

## Part 2 — template-specific checks

- **Bounded error envelope.** Every error response is `shared.WriteJSONError`/`WriteJSONErrorPayload` with a bounded `ErrType*` constant — never `http.Error`, never `err.Error()` in the `error` field. `grep -rn 'http.Error\|WriteJSONError' <scope>`.
- **Strict decoding.** New request bodies decode through the strict path (`DisallowUnknownFields` + trailing-data rejection). Confirm the decode helper is used, not a bare `json.Decode`.
- **Inputs validated at the documented layer.** Length limit lives in the service (`shared.LogMaxChars`), not duplicated in struct tags; pagination via `shared.ValidatePagination`.
- **UUID identity boundary respected.** No new code reads the raw subject and passes it to a query; it goes through `UserIDFromContext`.
- **No `os.Getenv` outside `internal/config`.** `grep -rn 'os.Getenv' <scope> | grep -v internal/config | grep -v _test.go` → nothing (the only documented exception is the healthcheck CLI flag).
- **Migrations.** New SQL is a *new* file (applied files are immutable); destructive subcommands stay behind the `MIGRATOR_ALLOW_DESTRUCTIVE` + `--confirm-destructive` gates; large-table indexes note `CONCURRENTLY`; `updated_at` is owned by the trigger, never `SET updated_at = NOW()`.
- **Dependencies.** Any new module in `go.mod` is justified in the report (what it's for, why not stdlib) — a supply-chain decision, not a default.

## Part 3 — run the scanners (report exact results)

Run and quote the output:

```bash
make ci                                  # build + vet + lint (incl. gosec) + race unit tests
make vulncheck                           # govulncheck — known CVEs
make semgrep                             # security anti-patterns
pre-commit run gitleaks --all-files      # committed secrets (now scans .claude/ too)
```

Note which need podman (none of these do). If a scanner isn't installed, say so — do not imply it passed.

## Output format

1. **Summary table** — `Rule/Check → Verdict → one-line citation`.
2. **Findings** — each Gap with file:line, why it breaks the rule, and the minimal fix. Intentional-by-decisions items listed separately.
3. **Commands run** — the scanner commands above with their actual result lines (pass/fail counts), or a note that one was unavailable.

## Non-negotiables

- **Cite evidence.** Every verdict needs a file:line, grep result, or scanner line.
- **Never weaken a rule to make code pass.** If the code violates a rule, the code is wrong — see `security.md`.
- **Respect [decisions.md](../../rules/decisions.md).** Don't raise a settled trade-off as a bug.
