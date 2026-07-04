---
name: write-unit-tests
description: Write unit tests that match this Go API template's test discipline. Use when the user says "write tests for X", "add unit tests", "cover this with tests", "test this handler/service/repository", or after adding/changing code that needs tests. Enforces table-driven subtests with useful got/want failures, behaviour-over-implementation assertions (wire responses, sentinel errors via errors.Is, envelope fields), the layer rule (pgxmock at the repository, interface fakes elsewhere, real router assembly for middleware-dependent handler tests), determinism (-race, no sleeps/wall-clock), and a tripwire proof for every guarded invariant. There is NO coverage-percentage gate and this skill must not add one — the bar is the behaviour checklist. Cross-references /security-review and the ASVS map for security behaviours.
---

# /write-unit-tests — tests that match this repo's discipline

This repo already has a house style for tests; new tests must look like the ones already here, not like generic Go tests. The reference suites are [`internal/userlog/userlog_test.go`](../../../internal/userlog/userlog_test.go) (handler + service), [`internal/userlog/userlog_service_test.go`](../../../internal/userlog/userlog_service_test.go) (pgxmock), [`internal/shared/logger/logger_test.go`](../../../internal/shared/logger/logger_test.go) (interface fake), and [`internal/userlog/userlog_integration_test.go`](../../../internal/userlog/userlog_integration_test.go) (build-tagged). **Cite the reference each rule mirrors — every checklist item below names the file that demonstrates it.**

## Inputs

- **Scope** — the function/type/package to test. If unstated, default to the package with uncovered new code in the working tree and say so.
- What invariant(s) the code guards (a `WHERE user_id = $N` scope, a 413 cap, a 401 on a bad subject). Each becomes a **tripwire** test (see below).

## The checklist (each with the repo model)

### 1. Table-driven subtests with useful failures
- One `[]struct` of cases, `t.Run(tc.name, …)` per case. Model: [`userlog_test.go`](../../../internal/userlog/userlog_test.go). See <https://go.dev/wiki/TableDrivenTests>.
- Every failure message states **got, want, and the input** — a bare `t.Errorf("failed")` is a defect. `t.Errorf("error field: got %q want body_too_large", env.ErrorType)` ([`userlog_test.go:943`](../../../internal/userlog/userlog_test.go#L943)) is the model. See <https://google.github.io/styleguide/go/decisions#useful-test-failures>.

### 2. Behaviour over implementation
- Assert on the **wire response** (status code, decoded JSON envelope fields), on **sentinel errors via `errors.Is`**, and on recorded metrics — never on private fields or call order. Models: the envelope-field assertions in [`userlog_test.go`](../../../internal/userlog/userlog_test.go), `errors.Is` checks in [`userlog_service_test.go`](../../../internal/userlog/userlog_service_test.go).
- The error `error` field must be the **bounded `ErrType*` constant** (e.g. `body_too_large`), asserted as such — this is how the bounded-label rule (security.md rule 7) stays enforced by tests.

### 3. Layer rules — pick the double by layer
- **Repository layer → `pgxmock`.** Real SQL string, mocked rows/errors. Models: [`userlog_service_test.go`](../../../internal/userlog/userlog_service_test.go), [`internal/db/db_test.go`](../../../internal/db/db_test.go). Assert the query ran with the expected args (this is where `user_id` scoping is proven).
- **Everywhere else → interface fakes**, not mocks of concretes. The recording logger — `testLogger(buf)` writing a `slog.NewJSONHandler` into a `bytes.Buffer`, then `decodeLines` to assert structured fields — is the model ([`logger_test.go:16`](../../../internal/shared/logger/logger_test.go#L16)).
- **Middleware-dependent handler tests → assemble the real router.** A handler whose behaviour depends on middleware (body-limit, auth context, metrics) must be exercised through the actual chi router, not by calling the handler func directly. **The 413 test `TestRequestBodyTooLarge` ([`userlog_test.go:885`](../../../internal/userlog/userlog_test.go#L885)) is the model** — it builds the router so the body-limit middleware is in the path, then asserts status `413`, the `body_too_large` envelope, and (via `assertErrorRecorded`, [`userlog_test.go:101`](../../../internal/userlog/userlog_test.go#L101)) that the bounded counter incremented.

### 4. Determinism
- **No `time.Sleep`, no wall-clock assertions.** Inject clocks/values; assert on outcomes, not on elapsed time.
- **DB-managed timestamps — seed, then assert the move.** To prove a database trigger sets a timestamp (e.g. `updated_at`), INSERT a row with a fixed, long-past sentinel value (the trigger fires on UPDATE, not INSERT, so the sentinel sticks), update through the code under test, then assert the value moved *off* the sentinel — a constant comparison, never a sleep or a two-write wall-clock ordering. Model: `TestLogRepository_CRUD_Integration` ([`userlog_integration_test.go`](../../../internal/userlog/userlog_integration_test.go)).
- **The unit suite always runs `-race`.** `make test-unit` is `go test -race ./...` ([`Makefile`](../../../Makefile)); write tests that pass under the race detector (no shared mutable state across goroutines without synchronisation).
- **Unit vs integration split by build tag.** Real-Postgres tests carry `//go:build integration` (model: [`userlog_integration_test.go`](../../../internal/userlog/userlog_integration_test.go)) and run only under `make test-integration`, which uses **`-p 1`** — packages share one Postgres and the migrator tests drop/re-apply the schema mid-run, so parallel packages would race the shared DB ([`Makefile`](../../../Makefile) `test-integration`). Never put a real-DB dependency in an untagged (unit) test.

### 5. The tripwire proof (mandatory for guarded invariants)
For any test that guards an invariant, **prove it actually guards** it:
1. Temporarily break the invariant in the code under test (drop the `AND user_id = $N`, raise the body cap, skip the UUID check).
2. Run the test; **watch it fail**.
3. Revert the break; confirm it passes again.
4. **Say so in your report** — name the invariant, the edit you made, and that the test failed then passed on revert. A test that stays green when the invariant is broken is not a test.

## Coverage stance (read this — do not add a gate)

There is **no coverage-percentage gate in this repo, and this skill must not add one.** Do not add `-coverprofile` thresholds, a `make cover` gate, or a CI coverage check. The bar is the **behaviour checklist**, not a number:
- the **happy path**,
- **every error path** the code can return (each mapped through `handleError` to its bounded `ErrType*`),
- **boundaries** (empty, max length, off-by-one on caps/limits, first/last page),
- and a **tripwire proof** wherever an invariant is guarded.
A package at 100% line coverage with none of these is under-tested; a package that covers all four is done regardless of the percentage.

## Security behaviours get tests as a matter of course
The template's security invariants are enforced *by tests*, and new security-relevant code must be too — as a matter of course, not on request. The canonical trio already present: **401 on a non-UUID subject**, **413 on an oversize body**, **400 on a malformed cursor**. When testing a change with a security surface, cross-reference [`/security-review`](../security-review/SKILL.md) and the [ASVS map](../security-review/references/asvs-map.md) for the requirements in scope, and add a test per relevant behaviour. (ASVS 5.0.0 itself lists automated testing as a use case for the standard.)

**Record the test in the ASVS map, in the same change.** When a test proves an ASVS requirement — newly, or by promoting a `met-untested` row — add its name to that row's **Test evidence** column in [`asvs-map.md`](../security-review/references/asvs-map.md) as part of the same change. The map's maintenance rule treats a `met` row with no named test as invalid (`met-untested`), so an untracked security test leaves the map lying about its own coverage.

## Verification
- `make test-unit` — the race-enabled unit suite (no podman). Run after writing.
- `make verify` — when the change has **integration surface** (new migration, new query, anything a real-Postgres test should cover); podman required. If podman is unavailable, run `make test-unit` and **say integration tests were not run** — never imply they passed.

## Output format
1. **What was tested** — the scope and the behaviour checklist items covered (happy / each error path / boundaries / tripwire).
2. **Tripwire proof** — the invariant, the break you introduced, "failed as expected", reverted.
3. **Commands run** — `make test-unit` (and `make verify` or the explicit no-podman note) with the actual result line.

## Non-negotiables
- **No coverage-percentage gate**, ever (see above).
- **Behaviour, not internals** — assert on wire/errors/metrics, not private fields.
- **`-race` clean and deterministic** — no sleeps, no wall-clock.
- **Tripwire every guarded invariant** and report it.
