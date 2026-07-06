---
name: architecture-review
description: Architecture-review a change or feature in this Go API template. Use when the user says "architecture review", "does this fit the architecture", "review the design of X", "is the layering right", or asks whether new code follows the repo's structural patterns. Checks layering and dependency direction (handler → service → repository), pattern conformance (Routes/RequiredTables, ErrType↔handleError, service-owned transactions), and single-source-of-truth (no literal duplicating a shared constant or spec value), reading .claude/rules/decisions.md FIRST and flagging any proposal that contradicts it. Produces a verdict table + improvements, each citing a file or a command result.
---

# /architecture-review: does this fit the layered design?

The template is a layered, one-package-per-feature architecture: **handler → service → repository**, with cross-cutting concerns in [`internal/shared/`](../../../internal/shared/) and **no cross-feature imports**. [`internal/userlog/`](../../../internal/userlog/) is the canonical shape. This skill checks a change against that structure and the settled decisions, citing a file or a command result for every verdict.

**Read [`.claude/rules/decisions.md`](../../rules/decisions.md) FIRST.** A proposal that contradicts a settled decision is flagged against that file, not adopted.

## Inputs

- **Scope**: a feature package (`internal/<feature>/`), the branch diff vs `main`, or the working tree. Default to the branch diff and say so.

## Verdict scale

- **Conforms**: matches the pattern, with a citation.
- **Minor drift**: works but deviates from the reference. Cite the difference and the fix.
- **Violates**: breaks a layering/dependency invariant or duplicates a single source of truth. Must fix.
- **Contradicts a decision**: proposes reversing a [decisions.md](../../rules/decisions.md) entry. Needs explicit user sign-off, not a silent change.

## Part 1: layering & dependency direction

| Invariant | How to prove it |
|---|---|
| Handler → service → repository only (no upward calls) | Read the feature: handler holds a service, service holds a repository. Repository imports neither. |
| **No feature imports another feature** | `grep -rn 'sud0x0/go-api-template/internal/' internal/<feature>/*.go \| grep -vE 'internal/(<feature>\|shared)' ` → nothing |
| `internal/shared` imports no feature | `grep -rn 'go-api-template/internal/' internal/shared/ \| grep -vE 'internal/shared'` → nothing |
| Infrastructure packages (`db`, `metrics`, `middleware`, `config`, `version`, `httpclient`) import no feature | `grep -rn 'internal/userlog\|internal/<feature>' internal/db internal/metrics internal/middleware internal/config` → nothing |
| `cmd/api/main.go` is the SOLE wiring exception | main may import every feature to wire it. Nothing else may. |

## Part 2: pattern conformance

- **Feature surface.** The package exposes `Routes(r chi.Router)` and `RequiredTables` (aggregated in `main.go`). `grep -rn 'func.*Routes\|RequiredTables' internal/<feature>/`.
- **Error contract is total.** Every `ErrType*` constant has a `handleError` case emitting the matching bounded label/status/envelope. **This is enforced by a contract test, run it as evidence:** `go test ./internal/<feature>/ -run 'ErrorType\|HandlerError\|Mappings'` (or the package's contract test), and `go test ./internal/contract/...`.
- **Transactions live in the service.** Multi-statement atomic operations use `db.WithTransaction` with a tx-scoped `NewLogRepository(tx)`. Single-statement methods are NOT wrapped (decisions.md #12). `grep -rn 'WithTransaction' internal/<feature>/`.
- **Logging placement.** Only the handler takes a logger. Service/repository don't.

## Part 3: single source of truth

A new literal that duplicates a shared constant or a spec value is a finding.

- Pagination sizes come from `shared.DefaultPageSize`/`MaxPageSize`/`MaxPageOffset`, batch cap from `shared.MaxBatchSize`, and length from `shared.LogMaxChars`. `grep -rn '\b100\b\|\b10000\b' internal/<feature>/` and confirm each is a named constant, not a re-hardcoded literal.
- Spec, README, and code stay in lockstep. **Run the contract tests as evidence** that documented defaults/maxima still equal the constants: `go test ./internal/contract/...`.
- Port/health defaults come from `config.DefaultPort` / `config.ResolvePort`, not re-typed strings.

## Part 4: check against settled decisions

Walk [`decisions.md`](../../rules/decisions.md). For each entry the change touches, confirm it still holds (e.g. cursors stay unsigned, offset stays a bare array, `metrics.New()` stays call-once). If the change reverses one, that is the headline finding. Surface it with the decision number and require explicit user sign-off.

## Output format

1. **Summary table**: `Invariant/Pattern → Verdict → citation`.
2. **Detail**: each drift/violation with the file:line and the minimal fix. Each contradicted decision with its number.
3. **Commands run**: the contract/handler test invocations with pass/fail lines (proof, not assertion).

## Non-negotiables

- **Cite a file or a command result for every verdict.** Run the contract tests rather than asserting they'd pass.
- **decisions.md is binding.** Don't "improve" a settled trade-off. Flag it and stop.
