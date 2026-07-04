---
name: security-review
description: Security-review a change, PR, or feature package in this Go API template against OWASP ASVS 5.0.0 (Level 2 bar). Use when the user says "security review this change/PR/feature", "is this secure", "audit the security of X", "check this for vulnerabilities", "ASVS review", or asks whether new code respects the repo's security rules. Identifies which ASVS chapters the change touches (via references/asvs-map.md), walks those requirements with requirement-ID-cited verdicts, keeps the seven rules in .claude/rules/security.md as the write-time rule set (each mapped to the ASVS IDs it implements), runs the template-specific checks (user_id scoping, bounded labels, strict decoding, UUID boundary, config-only env reads, migration gates, dependency justification) each annotated with its ASVS requirement, and runs the scanner suite. Produces a verdict table by ASVS area + findings with requirement IDs and file citations + the commands actually run.
---

# /security-review — review a change against OWASP ASVS 5.0.0 (Level 2)

This repo verifies against **OWASP ASVS 5.0.0 at Level 2** (L1 included by definition; L3-only out
of scope — [`decisions.md` #14](../../rules/decisions.md)). Two documents drive the review:

- **Write-time rule set:** [`.claude/rules/security.md`](../../rules/security.md) — the seven rules, unchanged, demonstrated in [`internal/userlog/`](../../../internal/userlog/).
- **Verification map:** [`references/asvs-map.md`](references/asvs-map.md) — every applicable L1/L2 requirement, its status, and the repo control that satisfies it. Built by reading the committed standard ([`references/asvs-5.0.0.txt`](references/asvs-5.0.0.txt) / PDF).

**Cite evidence for every verdict** — a `file:line`, a grep result, or a scanner line, plus the
**ASVS requirement ID** (reference form per the standard's *"How to Reference ASVS Requirements"*:
`V<n>` for a chapter, `<n>.<section>.<index>` for a requirement). "Looks fine" is not a verdict.

## Inputs

- **Scope** — the diff (`git diff main...HEAD`), a named package (`internal/<feature>/`), or the working tree. If unstated, default to the branch diff vs `main` and say so.
- Read [`decisions.md`](../../rules/decisions.md) first. A finding that contradicts a settled decision is **flagged against the map**, not adopted as a bug — say which entry, and if the map row disagrees with the code, the *map row* is what to reconcile.

## Verdict scale (matches the map)

- **met** — requirement satisfied by a cited control **AND named test(s) in the map's Test evidence column**. A `met` verdict with no test is not `met`.
- **met-untested** — the control exists in code but no test proves it; a finding, listed in Gaps. A `met` claim you cannot back with a test name is `met-untested`.
- **partial** — satisfied outside the app (edge/deployment) or only in part; note who owns the rest.
- **gap** — not satisfied; a finding. Cite the offending line and the minimal fix.
- **n/a** — the change has no surface for this requirement (say why).
- **deferred with auth** — V6/V7/V9/V10; applicable only once OIDC lands ([`decisions.md` #13](../../rules/decisions.md)).

## The seven rules → the ASVS requirements they implement

The seven rules are HOW you write secure code here; the ASVS IDs are WHAT that satisfies. Check the
rules against the scope, and record the requirement IDs in the output.

| Rule (security.md) | Implements (ASVS 5.0.0) | Check |
|---|---|---|
| 1 · Validate / parameterise / encode | 1.2.1, 1.2.3, **1.2.4** (parameterised SQL), 1.5.2, 2.2.1 | No string-built SQL; output via `json.Encoder`; only cleaning is `shared.SanitiseNullBytes`. `grep -rn 'fmt.Sprintf.*SELECT\|+ "SELECT\|Query(.*+' <scope>` → nothing |
| 2 · Type & validate every input | 2.2.1, 2.2.2, 1.3.3, 1.4.2, 15.3.5 | UUID path params via `uuid.Parse` (400 not 500); `rune_max`/`rune_min`/`rune_len` tags, never stdlib `max`/`min`/`len`. `grep -rn 'uuid.Parse\|rune_max\|rune_min\|rune_len' <scope>` |
| 3 · Validate DB output too | 15.3.1 | Every row read re-checked via `Model.Validate()`. `grep -rn '\.Validate()' <scope>` in read paths |
| 4 · Authenticate by default | **8.2.2** (identity boundary), 4.1.3, 14.2.1; V6 deferred (#13) | New routes under `/api/v1`; user ID via `shared.UserIDFromContext` (UUID-enforced 401), set via `shared.WithUserID`. Confirm routes sit inside the `r.Route("/api/v1", …)` block |
| 5 · Authorise in the repository | **8.2.2, 8.3.1, 8.4.1** | Every query `WHERE … AND user_id = $N`; no row filtering in the handler. `grep -rn 'user_id = \$' <scope>` — one per query |
| 6 · Validate file uploads | V5 (n/a until an upload exists) | Only if the scope adds uploads: MIME + extension + size, all three |
| 7 · Errors carry causes; labels bounded | **16.5.1, 16.3.4, 16.4.1** | Repos wrap multi-`%w`; every metric/`error_type`/`path` label is a Go constant / chi pattern — never `err.Error()`, raw SQL, or user strings. `grep -rn 'IncAPIError\|ErrType\|%w: %w' <scope>` |

## Workflow

1. **Scope → map areas.** Read the diff/package. List which ASVS chapters it touches, using
   [`asvs-map.md`](references/asvs-map.md) (e.g. a new query touches V8; a new request body touches
   V1/V2/V15; a new header/middleware touches V3/V13/V14; a new outbound call touches V12/V13/V15).
2. **Walk those requirements.** For each touched requirement, give a verdict with the **requirement
   ID + file citation + the test that proves it** (the map's Test evidence column). A `met` verdict
   you cannot back with a named test is a finding — record it `met-untested`. New code must keep
   every `met` row `met` *and tested*; if it regresses the control or its test, that's a finding. If
   it *closes* a `gap`/`partial`/`met-untested`, update that map row **and its evidence in the same
   change** (the map's maintenance rule — a stale row, or a `met` row with no evidence, is a bug).
3. **Template-specific checks** (each annotated with the ASVS requirement it operationalises):
   - **Bounded error envelope** (16.5.1) — every error via `shared.WriteJSONError`/`WriteJSONErrorPayload` with a bounded `ErrType*` constant; never `http.Error`, never `err.Error()` in the `error` field.
   - **Strict decoding** (15.3.3 mass-assignment, 1.5.2 safe deserialization) — request bodies decode through the strict path (`DisallowUnknownFields` + trailing-data rejection); body-size cap → 413 (2.4.1/15.2.2).
   - **Inputs validated at the documented layer** (2.2.1/2.2.2) — length limit in the service (`shared.LogMaxChars`), pagination via `shared.ValidatePagination`, not duplicated in struct tags.
   - **UUID identity boundary** (8.2.2, 4.1.3) — no new code reads the raw subject and passes it to a query; it goes through `UserIDFromContext`.
   - **No `os.Getenv` outside `internal/config`** (13.3.1 secret handling, V13 config) — `grep -rn 'os.Getenv' <scope> | grep -v internal/config | grep -v _test.go` → nothing.
   - **Migrations** (13.4.x, 15.2.3) — new SQL is a *new* file (applied files immutable); destructive subcommands stay behind the `MIGRATOR_ALLOW_DESTRUCTIVE` + `--confirm-destructive` gates; `updated_at` owned by the trigger.
   - **Dependencies** (15.1.1 remediation, 15.1.2 SBOM/inventory) — any new `go.mod` module is justified in the report (what it's for, why not stdlib) — a supply-chain decision.
4. **Run the scanners** (report exact output):
   ```bash
   make ci                                  # build + vet + lint (incl. gosec) + race unit tests — 1.2.4, 1.4.2, 15.x
   make vulncheck                           # govulncheck — known CVEs — 15.1.1 / 15.2.1
   make semgrep                             # security anti-patterns — V1 / V15
   pre-commit run gitleaks --all-files      # committed secrets (scans .claude/ too) — 13.3.1
   ```
   None need podman. If a scanner isn't installed, say so — do not imply it passed.

## Output format

1. **Verdict table by ASVS area** — `Requirement ID → Verdict → one-line citation`, grouped by chapter, for the areas the change touches.
2. **Findings** — each `gap`/regression with requirement ID, `file:line`, why it breaks the requirement, and the minimal fix. Items that are intentional per `decisions.md` are listed separately and flagged against the map, not raised as bugs.
3. **Map deltas** — any row this change moves (e.g. `gap → met`), confirming the map was updated in the same change.
4. **Commands run** — the scanner commands with their actual result lines (pass/fail counts), or a note that one was unavailable.

## Non-negotiables

- **Cite evidence + requirement ID + test.** Every verdict needs a `file:line`/grep/scanner line AND an ASVS ID; a `met` verdict additionally needs the **test name** that proves it. A `met` claim with no test is `met-untested` — a finding, never silently `met`.
- **Never weaken a rule to make code pass.** If the code violates a rule, the code is wrong — see [`security.md`](../../rules/security.md).
- **Respect [decisions.md](../../rules/decisions.md).** A settled trade-off (edge-first TLS/rate-limiting, unsigned cursors, deferred auth) is not a bug — it's a `partial`/`deferred` row in the map.
- **Keep the map current.** Any change that touches a mapped area updates its rows in the same change; the review confirms it did.
- **The bar is Level 2.** Do not raise L3-only requirements as gaps; do not drop an L1 requirement because it's "obvious".
