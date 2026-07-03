---
name: performance-review
description: Performance-review a change or endpoint in this Go API template. Use when the user says "performance review", "is this efficient", "optimise X", "why is X slow", or asks about query plans, indexes, hot paths, or load behaviour. Core rule is measure-before-changing: no optimisation lands without a number. Checks query plans (EXPLAIN ANALYZE, BUFFERS), index alignment, keyset vs offset for unbounded data, bounded work (limits/timeouts/caps/body size), hot-path sanity (nothing per-request that should be per-process), and names the metrics to read. Produces findings with evidence (plan, metric, or measurement) attached; anything without evidence is labelled a hypothesis, not a finding.
---

# /performance-review — measure before you change

**Core rule, stated up front: no optimisation lands without a number.** Every claim in the output is backed by a query plan, a metric reading, or a measurement. A change "that should be faster" with no measurement is a **hypothesis**, labelled as such — never a finding. The precedent for recording evidence already lives in the repo: [`queryGetLogsKeysetAfter`](../../../internal/userlog/userlog_repository.go) carries its `EXPLAIN (ANALYZE, BUFFERS)` plan in a comment at the query constant.

## Inputs

- **Scope** — an endpoint, a query, a package, or the branch diff. Default to the branch diff vs `main` and say so.
- A reachable dev stack for plans/metrics: `make run` (PODMAN REQUIRED). If podman is unavailable, say which checks could not be run — do not guess plans.

## Part 1 — queries (the usual cost centre)

For every new or changed SQL query:

- **Get the plan.** Run `EXPLAIN (ANALYZE, BUFFERS) <query>` against the dev DB with realistic params (seed enough rows that a Seq Scan would lose to an Index Scan — the existing plan note used 5000). Record non-trivial plans in a comment at the query constant, matching the `queryGetLogsKeysetAfter` precedent.

  ```bash
  podman exec -i app_db psql -U "$DB_USER" -d "$DB_NAME" -c \
    'EXPLAIN (ANALYZE, BUFFERS) SELECT …;'
  ```
- **Index alignment.** The `WHERE` + `ORDER BY` must be servable by an existing index (e.g. `idx_logs_user_datetime_id` = `(user_id, date_and_time DESC, id DESC)`). A plan showing `Seq Scan` or a `Sort` node on a hot list path is a finding.
- **Unbounded data uses keyset.** A list that can grow without bound should offer cursor (keyset) paging, not deep offset (offset has a documented hard cap, `shared.MaxPageOffset`). See decisions.md #1–2.

## Part 2 — bounded work

Every new endpoint must bound its work — confirm each:

- **Row limits.** Pagination `limit` is validated and capped (`shared.MaxPageSize`); no unbounded `SELECT`.
- **Timeouts.** The request timeout middleware applies; long operations respect context.
- **Batch caps.** Multi-row writes are capped (`shared.MaxBatchSize`).
- **Body size.** Request bodies are capped (the 1 MiB `MaxBytesReader` → 413). A new body-reading endpoint without the cap is a finding.

## Part 3 — hot-path sanity

Nothing per-request that should be per-process:

- **No per-request compiles/parses.** `grep -rn 'regexp.MustCompile\|template.Must\|regexp.Compile' <scope>` — these belong at package scope, not inside a handler.
- **Allocations in middleware.** Per-request middleware should avoid avoidable allocations on every call; bind-once patterns (like the logger's `WithRequestContext`) are preferred.
- **N+1 and round-trips.** Multi-row writes note the `pgx.Batch` deferral (decisions.md #6) — propose batching ONLY with a measurement showing the round-trips matter.

## Part 4 — what to measure with

The instrumentation already exists; name the signal and how to read it locally:

- `http_request_duration_seconds` (histogram, by method/path/status) — endpoint latency.
- `db_query_duration_seconds` (by operation) — query cost from the app's side.
- `db_pool_*` (from the pool collector) — saturation: in-use vs idle vs total.
- Read them at `curl -s localhost:9090/metrics | grep <name>` on the running stack.
- **Optional quick load probe:** if `hey` or `bombardier` is already installed, a short run gives a latency distribution (`hey -z 10s -c 20 http://localhost:8080/api/v1/logs`). **Never add them as project dependencies** — they're ad-hoc local tools only.

## Output format

1. **Findings** — each with its evidence inline: the plan excerpt, the metric value, or the measurement. State the before/after number for any proposed change.
2. **Hypotheses** — plausible improvements with NO measurement yet, clearly separated and labelled. Each names the number that would confirm or kill it.
3. **Commands run** — the EXPLAIN / metric / load commands and their output; note any skipped because podman was unavailable.

## Non-negotiables

- **A finding has a number.** No plan / metric / measurement → it's a hypothesis, not a finding.
- **Record plans where the code lives** — a comment at the query constant, like the keyset precedent.
- **Respect [decisions.md](../../rules/decisions.md):** `pgx.Batch`, OpenTelemetry, and the offset/cursor split are deferred/settled — reopen them only with evidence and user sign-off.
