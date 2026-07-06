# Coding security rules

Non-negotiable. Read before writing any handler, repository, middleware, or migration.

## The hierarchy (OWASP)

> Validate on input, parameterise on storage, encode on output: at each language boundary, using the tool that owns that boundary.

Input sanitisation sits *below* those three in OWASP's defence hierarchy ([Input Validation Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Input_Validation_Cheat_Sheet.html), [XSS Prevention Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Cross_Site_Scripting_Prevention_Cheat_Sheet.html)). Use it ONLY when the destination language structurally cannot hold the input. This codebase's only such case is `shared.SanitiseNullBytes`, because Postgres `TEXT` rejects `\x00`. Never use sanitisation as a substitute for validation, never to "neutralise" XSS (that belongs at the renderer's templating engine), and never as a "be safe" reflex on every string. It silently mutates user data and, when applied repeatedly across round-trips, causes double-encoding bugs that are painful to debug.

## The seven rules

1. **Validate strictly, parameterise queries, encode on output.** The API stores exactly what the client sent. XSS prevention lives at the renderer. The API relies on `json.Encoder`'s default HTML escaping (`<` → `<`) plus parameterised pgx queries. The only input cleaning is `shared.SanitiseNullBytes`.

2. **Type and validate every input.** UUID path params via `uuid.Parse` so a malformed id never reaches Postgres (returns 400, not 500). Length limits via `rune_max` / `rune_min` / `rune_len` tags (registered through `shared.RegisterRuneLenValidators`). Rune count matches Postgres `VARCHAR(N)` semantics. Never use the stdlib `max` / `min` / `len` tags for user-facing text.

3. **Validate database output too.** Every row read from Postgres is re-checked via `Model.Validate()`. This is defence in depth. A schema migration drift surfaces here, not later in business logic. Repositories that skip this step are wrong.

4. **Authenticate by default.** Every API endpoint requires authentication unless explicitly excluded. The auth contract: any middleware that stores the user ID on the request context with `shared.WithUserID(ctx, userID)` works. Handlers read it back via `shared.UserIDFromContext`. The key lives in `internal/shared`, so infrastructure middleware never imports a feature package. The user ID **MUST be a UUID** (the `user_id` column is `UUID NOT NULL`): `UserIDFromContext` is the single enforcement point. It canonicalises the value and rejects non-UUIDs as 401, so a bad subject never reaches a query as a cast-error 500. An IdP whose `sub` is not a UUID needs a mapping to an internal UUID, resolved in the middleware before `WithUserID` (`WithUserID` is a plain setter, and validating only at the read point keeps a second writer from bypassing it). The template implements this two-layer model at the `r.Route("/api/v1", …)` block in `cmd/api/main.go`: layer 1 is OIDC token validation (`internal/middleware/auth_middleware.go`, which maps `iss|sub` to a UUIDv5), layer 2 is OPA function-level authorisation (`internal/middleware/authz_middleware.go`, fail-closed). Both are opt-in and off by default. Row-ownership (`WHERE user_id = $N`, rule 5) remains the layer beneath OPA's function-level checks. See `decisions.md` #13 and #16–#19.

5. **Authorise in the repository.** Every query has `WHERE … AND user_id = $N`. No filtering in the handler that the database can do. The repository is where row-ownership is enforced. The handler trusts the user ID set by auth middleware.

6. **Validate file uploads.** MIME type, file extension, size limit (all three). None are implemented in the template. Add them at the point of adding an upload endpoint, not after.

7. **Errors carry causes, labels are bounded.** Repository errors wrap with multi-`%w` (`fmt.Errorf("getX %s: %w: %w", id, ErrDatabase, err)`) so `errors.Is(err, ErrDatabase)` matches AND the handler can log the real cause. Every metric label that's at risk of unbounded cardinality (`api_errors_total{feature,error_type}`, `db_query_*{operation}`, HTTP `path`) comes from package-level Go constants, chi route patterns, or a fixed allow-list. Never `err.Error()`, never raw SQL, never raw URLs, never user-supplied strings.

## Reading further

- `internal/userlog/` is the reference implementation of all seven rules. When in doubt, mirror what it does.
- The README's "Coding rules" section is the abbreviated version of this file for human readers. This file is the source of truth for code generation.
