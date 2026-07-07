---
name: new-feature
description: Scaffold a new feature package (handler + service + repository + migration + OpenAPI + CHANGELOG entry) in this go-api-template repo. Use when the user wants to add a new domain like "orders", "users", "products", anything that owns its own table and CRUD endpoints. Copies internal/userlog/ as the canonical pattern, renames every symbol, writes a goose migration, registers the table via the feature's RequiredTables (aggregated in cmd/api/main.go), wires constructors and a Routes method in cmd/api/main.go, and records the change in CHANGELOG.md.
---

# /new-feature: scaffold a new feature package

This codebase organises features as self-contained packages under `internal/<name>/`. Each package owns one bounded slice of HTTP + business + persistence logic. [`internal/userlog/`](../../../internal/userlog/) is the reference. Copy it.

**Read [`.claude/rules/security.md`](../../rules/security.md) before writing any code in this skill.**

## Inputs to collect

Ask the user for:

1. **Feature name**: singular, lowercase, must NOT shadow a stdlib package (avoid `log`, `time`, `json`, `http`, `os`, `errors`, `context`, `sync`, etc.). Example: `order`.
2. **Main type name**: PascalCase. Example: `Order`.
3. **Migration number**: next free under `migrations/`. Find it with `ls migrations/` and increment the highest 5-digit prefix.
4. **Schema sketch**: at minimum, the table fields and their types/nullability. If the user hasn't given them, ask.

## Steps

### 1. Copy and rename

```bash
cp -r internal/userlog internal/<feature>
cd internal/<feature>
for f in userlog_*.go; do mv "$f" "${f#userlog_}"; done
sed -i 's/userlog/<feature>/g; s/\bLog\b/<Type>/g' *.go
```

**Known sed edge cases: check every one by hand after the bulk replace.** The
`s/userlog/<feature>/g; s/\bLog\b/<Type>/g` pass is blunt. These are the spots it
reliably gets wrong:

- [ ] **Struct field named after the type.** A field like `Log string` inside a
  struct becomes `<Type> string` and collides with the type name or reads wrong.
  Rename the field to the domain meaning (e.g. `Body`, `Content`), not the type.
- [ ] **JSON tag values.** `s/\bLog\b/<Type>/` is case-sensitive, so it does NOT
  touch the lowercase `log` inside `json:"log"` (and `validate:"required"` has no
  `Log` at all). Those tags are left as `log`. Decide whether the wire field
  should be renamed, and if so do it deliberately (it changes the API contract).
- [ ] **Prose comments containing the old name.** Comments mentioning "log",
  "logs", or "Log" in sentences get partially rewritten or left stale. Re-read
  every comment in the copied files.
- [ ] **Test fixture strings.** Literal strings in tests (`"Test Log Entry"`,
  `"updated content"`, table-test case names, expected error substrings) are
  data, not code. Sed may or may not touch them and either way they likely need
  domain-appropriate values.
- [ ] **SQL identifiers.** Table/column names in the repository's query
  constants (`FROM logs`, `idx_logs_*`) must match your migration exactly.
- [ ] **`FeatureName` and `RequiredTables`.** `FeatureName = "log"` (the metric
  label) and `RequiredTables = []string{"logs"}` must become your feature/table
  names.

Then review each file by hand. Sed handles the bulk but leaves these edge cases:

- **`<feature>_model.go`**: replace the `Log` struct fields with the user's schema. Keep `Validate()` re-checking every field (rule 3). Keep `CreateRequest` and `UpdateRequest` DTOs with the right `validate:` tags.
- **`<feature>_errors.go`**: keep both the sentinel `Err*` errors AND the `FeatureName` + `ErrType*` constants. The latter label `api_errors_total`. Rename `FeatureName = "log"` to your feature name.
- **`<feature>_repository.go`**: replace SQL with your queries. Use parameterised queries (`$1`, `$2`, …). Never string-concatenate. Keep multi-`%w` error wrapping: `fmt.Errorf("getX %s: %w: %w", id, ErrDatabase, err)`. Keep the `len(userID) > repoMaxUserIDLength` guards returning `ErrInvalidInput`. Keep `WHERE … AND user_id = $N` on every query (rule 5).
- **`<feature>_service.go`**: pure business logic. No HTTP, no SQL. Update the rune-count limits and date validation as your domain requires.
- **`<feature>_handler.go`**: keep the flow: UUID parse → struct validate → service call → `responseJSON`. Every error path through `handleError`. Every metric label from constants.
- **`<feature>_test.go`**: update test data and assertions. Keep the `assertErrorRecorded` calls in every error-path subtest so the bounded-label discipline (rule 7) is enforced.

**Cross-user access (only if your feature needs it).** If a feature must let a privileged caller act on ANOTHER user's rows, follow the `userlog` reference pattern exactly (`decisions.md` #21), do not invent a new mechanism:

- The OPA middleware already authorises `{subject, roles, action, resource, method, target_user}` in the four fixed verbs (`read`/`create`/`update`/`delete`) and writes the authorised `target_user` to the context after an explicit allow. Do not add new action names.
- Add a **target-aware repository method** (`getXForTarget`, `updateXForTarget`, …) that runs the SAME query scoped to the **target's** `user_id`. Never add an unscoped query that omits `user_id`: cross-user still enforces object ownership at the data layer.
- The handler defaults to **self-access** (the caller's UUID) and goes cross-user ONLY via `effectiveOwner(ctx, caller)` (reads `shared.TargetUserFromContext`, set only after the OPA allow). With OPA disabled every request stays self-access, so the cross-user branch cannot be reached without the policy check.
- Create stays caller-owned (no cross-user create). Add the `?user=` parameter to the OpenAPI spec for read/update/delete only, and a role-or-attribute rule to `policy.rego` + `policy_test.rego`. See `internal/userlog/` for the full worked example.

### 2. Write the migration

Create `migrations/NNNNN_create_<features>.sql`. Use goose markers:

```sql
-- +goose Up
-- +goose StatementBegin
CREATE TABLE <features> (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID        NOT NULL,
    -- … your fields …
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_<features>_user_id ON <features>(user_id);
-- Add other indexes only where a query actually filters/sorts on those columns.

-- The update_updated_at_column() trigger function already exists (migration 00001).
-- Reuse it — BEFORE UPDATE triggers run before RETURNING is evaluated, so the
-- application sees the fresh timestamp. NEVER also SET updated_at = NOW() in UPDATEs.
CREATE TRIGGER update_<features>_updated_at
    BEFORE UPDATE ON <features>
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_<features>_updated_at ON <features>;
DROP TABLE IF EXISTS <features> CASCADE;
-- +goose StatementEnd
```

**Never edit an applied migration.** Once `00001_*` has run against any shared/production database, fixes go in a new migration file.

### 3. Register the table

Each feature owns the list of tables it needs. In your new `<feature>_repository.go`
(copied from userlog), set:

```go
// RequiredTables names the database tables this feature needs.
var RequiredTables = []string{"<features>"}
```

Then add it to the aggregation in [`cmd/api/main.go`](../../../cmd/api/main.go):

```go
requiredTables := slices.Concat(
    userlog.RequiredTables,
    <feature>.RequiredTables,   // add this line
)
```

Do NOT edit `internal/db/db.go`. Schema verification is decoupled from that core
package on purpose. The API checks the aggregated list at startup and refuses to
boot if a required table is missing.

### 4. Wire constructors in main.go

In [`cmd/api/main.go`](../../../cmd/api/main.go), next to the existing `userlog.New*` calls:

```go
<feature>Repo := <feature>.New<Type>Repository(database.Pool())
<feature>Svc  := <feature>.New<Type>Service(<feature>Repo, database)
<feature>H    := <feature>.New<Type>Handler(<feature>Svc, appLogger, appMetrics)
```

Only the handler takes the logger. The repository and service do not log (they
return wrapped sentinel errors and let the handler log/count them), so their
constructors take no logger.

**Two service shapes (single-row vs multi-statement).** The service takes BOTH a
repository (for single-row paths) AND a transaction runner (`*db.DB`, which
satisfies a small `txRunner` interface):

- **Single statement → no transaction.** Each single-row method (`get`,
  `create`, `update`, `delete`) is one SQL statement, which is already atomic.
  Call the repository field directly. Do NOT wrap it in a transaction. That
  would add a round-trip for nothing.
- **Two or more statements that must commit together → transaction in the
  service.** This is THE canonical pattern (see `userlog.createLogs`). The
  service runs `db.WithTransaction(ctx, func(tx pgx.Tx) error { … })` and, inside
  the closure, builds a repository over the `pgx.Tx`: `New<Type>Repository(tx)`.
  Repositories accept `db.PgxIface`, which `pgx.Tx` satisfies, so the SAME
  repository code runs inside and outside a transaction. Any error returned from
  the closure rolls back the whole unit. Transaction control lives in the
  SERVICE layer. Repositories stay transaction-agnostic.

If your feature has no multi-statement operation yet, still pass `database` to
the constructor (it is cheap and keeps the shape ready), or drop the `txRunner`
field until you need it.

Each feature registers its own routes via a `Routes(r chi.Router)` method on its
handler (copied from `userlog.Handler.Routes`, rename the paths and handler
methods). Mount it with a single line inside the existing `r.Route("/api/v1", …)`
block:

```go
<feature>H.Routes(r)
```

Do NOT scatter five `r.Get`/`r.Post` lines in main.go. Keep route registration
next to the handlers so the feature owns its URL shape and main.go gains exactly
one line per feature.

No `Close()` is needed in the shutdown path. pgx auto-prepares per connection. There are no manual resources to release.

**Listing & pagination.** If your feature has a list endpoint that can grow past
a few thousand rows, add **keyset (cursor) pagination**, copy it from
`userlog`: the keyset queries (`getLogsKeysetFirst` / `getLogsKeysetAfter`), the
opaque cursor codec (`userlog_cursor.go`: base64url of the sort key, validated
strictly on decode → `400`), and the service's `getLogsCursor` (decode → fetch
`limit+1` → emit `next_cursor`). Add the composite
`(user_id, <sort_col> DESC, id DESC)` index in your migration (the `id`
tiebreaker makes the keyset unique). Offset pagination stays for shallow
browsing but is capped at `shared.MaxPageOffset` (10000) as a hard depth limit.
Keep the response-shape asymmetry documented: bare array for offset, wrapped
`{"items":[…],"next_cursor":"…"}` for cursor mode.

### 5. Document the API

Update [`api/v1/openapi.yaml`](../../../api/v1/openapi.yaml):

- Add `<Type>`, `<Type>CreateRequest`, `<Type>UpdateRequest` schemas (mirror the existing `Log*` shapes).
- Add the five paths under `paths:`.
- Add a `tags:` entry for the new feature.

### 6. Record the change

Add an entry under `## [Unreleased]` in [`CHANGELOG.md`](../../../CHANGELOG.md):

```markdown
### Added

- `<Type>` feature: CRUD endpoints under `/api/v1/<features>` with migration NNNNN.
```

The release workflow fails if `## [X.Y.Z]` is missing at tag time, keep `[Unreleased]` current.

## Verification

After scaffolding, in order:

```bash
make run                      # bring up dev stack if not already
make db-migrate               # apply the new migration in dev
make db-status                # confirm NNNNN is `applied`
```

If `make db-migrate` fails, your migration is wrong. Fix it before running integration tests, never paper over with a manual SQL fix in dev.

Then **review the new package** with the two review skills, and fix what they raise:

- `/security-review internal/<feature>/`: the seven rules mapped to OWASP ASVS 5.0.0 (Level 2) via the applicability map + template-specific checks (user_id scoping, bounded labels, strict decoding, UUID boundary) and the scanner suite.
- `/architecture-review internal/<feature>/`: layering, the `Routes`/`RequiredTables` surface, the `ErrType*` ↔ `handleError` contract test, and single-source-of-truth.

Write the package's tests and comments to the house style while you go:

- `/write-unit-tests internal/<feature>/`: table-driven subtests, behaviour-over-implementation assertions, the layer rule (pgxmock at the repository, the real-router 413 test as the model), `-race`, and a tripwire proof for each guarded invariant (the `user_id` scope, the body cap). No coverage-percentage gate.
- `/write-comments internal/<feature>/`: why-not-what, evidence comments, `decisions.md` citations, `Template surface:` markers, and `TODO(scope): … — <pointer>`. Keep every comment in sync with the code in the same change.

Finally, run the **verification loop from [CLAUDE.md](../../../CLAUDE.md)**: `make ci` (no podman) after every change, and `make verify` (podman: `ci` + integration tests) before finishing. **Do not commit**, per the never-commit rule, leave the new feature uncommitted and, in your report, propose a Conventional Commit message for the owner (e.g. `feat(<feature>): add CRUD endpoints under /api/v1/<features>`).

## Non-negotiables

- **No cross-feature imports.** `internal/<feature1>/` must never import `internal/<feature2>/`. Shared concerns go in `internal/shared/`.
- **Read [`.claude/rules/security.md`](../../rules/security.md).** All seven rules apply to every file in the new package.
- **Bounded labels only.** Every `Inc()` on a `*CounterVec` takes values from `const` strings, route patterns, or a fixed allow-list. Never `err.Error()`, never raw URLs, never user-supplied strings.
- **`internal/userlog/` is the reference.** If you're unsure how to do something, look at what userlog does, and if userlog doesn't do it, you probably don't need to either.
