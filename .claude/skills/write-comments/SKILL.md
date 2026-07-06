---
name: write-comments
description: Write and maintain comments the way THIS repo does — comments are load-bearing context for the next reader (human or agent), and a stale comment is a bug. Use when the user says "comment this", "add comments", "document this code", "improve the comments", "add a doc comment", or when reviewing whether new code's comments meet the repo's bar. Enforces Go doc-comment conventions (identifier-first, full sentences, package docs), why-not-what for anything non-obvious, evidence comments for verified claims, decisions.md citations where a comment defends a settled trade-off, the "Template surface:" marker for deliberately-uncalled code, the same-change rule (a behaviour change updates every comment that describes it, in the same commit), and a TODO(scope) policy. This repo does NOT follow the generic "good code needs no comments" line — that would strip exactly the comments it depends on.
---

# /write-comments — comments as load-bearing context

**Philosophy (read this first).** In this repo, comments are not decoration and not a code smell.
They are the durable record of *why* the code is the way it is — the trade-off it defends, the
measurement that justifies it, the contract the next author must not break. The next reader is
often an agent with no memory of the decision. So the bar here is the **opposite** of "good code
needs no comments": under-commenting is a defect, and a **comment that has drifted out of sync with
the code is a bug** — the same severity as a wrong line of code, because it actively misleads.

The models to imitate are already in the tree; each rule below cites one.

## The rules (each with the repo model)

### 1. Go doc-comment conventions
- A doc comment on an exported identifier **begins with the identifier name** and is one or more
  **full sentences**: `// SecurityHeaders returns middleware that …` ([`internal/middleware/security_middleware.go`](../../../internal/middleware/security_middleware.go)).
- **Every package has a package doc comment.** See <https://go.dev/doc/comment>.
- Punctuate and capitalise like prose — these render on `pkg.go.dev` and in editors.

### 2. Why-not-what for anything non-obvious
- Comment the **reason**, not a restatement of the statement. `WriteJSONErrorPayload`'s comment
  explains *why* the payload is buffered before the status is written (a marshal failure must not
  leave a half-written response), not *that* it calls `Encode` ([`internal/shared/response.go`](../../../internal/shared/response.go)).
- **Evidence comments** — when a claim was actually verified, record the evidence so nobody has to
  re-derive it. The recorded `EXPLAIN (ANALYZE, BUFFERS)` plan above the keyset query
  (`queryGetLogsKeysetAfter`, [`internal/userlog/userlog_repository.go`](../../../internal/userlog/userlog_repository.go)) is **the model**: it names the index used, the row count tested, and the Postgres version.
- **Cite `decisions.md` when a comment defends a settled trade-off** so the next reader finds the
  full rationale instead of re-opening it: e.g. the readiness cache and the unsigned-cursor code
  point at their [`decisions.md`](../../rules/decisions.md) entries. Link the entry number.

### 3. The `Template surface:` marker
Code that has **no in-repo production caller by design** (a seam an adopter wires up, a utility the
template ships but doesn't itself use) carries a `Template surface:` doc marker explaining why it
looks dead. Models: [`internal/httpclient/httpclient.go`](../../../internal/httpclient/httpclient.go), [`internal/shared/identity.go`](../../../internal/shared/identity.go). `make deadcode` expects exactly these markers — an unmarked unreachable export is a finding; a marked one is intentional.

### 4. The same-change rule
**Any behaviour change updates every comment and doc that describes that behaviour, in the same
commit.** If you change the body-size cap, the `413`, a query plan, a validation limit, or a
default, the comments (and the OpenAPI spec, and any `decisions.md`/README line that states the old
value) change with it. Shipping code and its stale comment in the same diff is the bug this rule
exists to prevent. (This mirrors the [ASVS map's](../security-review/references/asvs-map.md) maintenance rule for its own rows.)

### 5. What NOT to comment
- **Narration of the obvious.** `i++ // increment i`, `// return the result` — delete on sight.
- **Restating the signature.** `// f takes an int and returns a string` adds nothing the
  declaration doesn't already say. Comment the *why* or the *contract*, not the types.
- **Changelog-style history inside code.** `// 2026-03: changed by X to fix Y`. History lives in
  git and `CHANGELOG.md`, not in a comment that will rot. Describe the code as it is now.

### 6. TODO policy
Format: **`TODO(scope): description — <pointer>`**, where `<pointer>` is one of:
- a tracking issue (`— #123`),
- a [`decisions.md`](../../rules/decisions.md) entry (`— see decisions.md #13`), or
- a named, deliberate deferral documented somewhere a reader can find.

**A TODO without a pointer is a finding** — it's an orphaned intention nobody will ever action.
`scope` is the area (`auth`, `perf`, `migration`). The pointer form is what matters: a TODO like
`TODO(perf): batch these inserts — see decisions.md #6` names its scope and cites where the deferral
is justified, so a reader can find the rationale instead of guessing.

## Verification
Comments have no compiler, so check them by reading:
- `gofmt`/`go vet` (run by `make ci`) catch malformed doc comments and some `//go:` directives.
- For a behaviour change, **grep the old value/word** across the repo (`grep -rn '<old constant/limit/status>' .`) to prove no comment, spec, or doc still states the superseded behaviour — that's how you satisfy the same-change rule.
- `make deadcode` (occasional) confirms every unreachable export still carries its `Template surface:` marker.

## Output format
1. **What changed** — the comments added/updated and the rule each satisfies (why-not-what, evidence, decisions cite, Template surface, TODO).
2. **Same-change check** — for any behaviour change, the grep proving no stale comment/spec/doc remains.
3. **TODOs** — any `TODO(scope): … — <pointer>` added, with the pointer; flag any pointer-less TODO found in the scope as a finding.

## Non-negotiables
- **A stale comment is a bug** — fix it in the same change as the code it describes.
- **Why, not what** — never narrate the obvious or restate the signature.
- **Every TODO has a pointer**, in `TODO(scope): … — <pointer>` form.
- **`Template surface:`** on every deliberately-uncalled export; nothing else explains it to `make deadcode`.
