# ASVS 5.0.0 applicability map — go-api-template

**Verification bar: Level 2** (all L1 requirements are included by definition; L3-only
requirements are out of scope). Standard: **OWASP ASVS 5.0.0** (*Version 5.0.0, May 2025*).
The official machine-readable CSV (`OWASP_Application_Security_Verification_Standard_5.0.0_en.csv`)
is committed under [`references/`](.) as the upstream source, with
[`asvs-5.0.0.txt`](asvs-5.0.0.txt) as its greppable plain-text rendering.

> Contains material from the OWASP Application Security Verification Standard 5.0.0,
> © 2008–2025 The OWASP Foundation, licensed CC BY-SA 4.0
> (<https://owasp.org/www-project-application-security-verification-standard/>). Requirements
> below are **paraphrased**, not reproduced. See [`ATTRIBUTION.md`](ATTRIBUTION.md).

**Maintenance rule (this map is a working document, not a snapshot):** any change that touches a
mapped area **updates its rows AND their test evidence in the same change**. A stale row is a bug —
the same class of defect as a stale comment. **A `met` row without a value in the Test evidence
column is invalid**: a `met` status is a claim, and an unverified claim can silently regress, so a
`met` control with no test naming it is demoted to `met-untested` and listed under Gaps. The
two-layer auth model has landed (see [`decisions.md` #13/#16–#19](../../../rules/decisions.md)), so
chapters **V9** and **V10.3/V10.5** now have per-requirement `met` rows (resource-server token
validation and identity), while the authorization-server / login-flow requirements
(V10.1/V10.2/V10.4/V10.6/V10.7, nonce, back-channel logout) are honest `n/a` rows with the
adopter-IdP/frontend reason, and V6/V7 are IdP-owned.

**Status vocabulary:** `met` (a template control satisfies it, cited, **with a named test in the
Test evidence column**) · `met-untested` (met in code but no test proves it — a finding; the
behaviour can regress undetected, so it is listed under Gaps) · `partial` (partially satisfied or
satisfied outside the app, e.g. at the edge) · `gap` (not satisfied; a finding for the owner to
schedule) · `n/a` (no surface in the template; reason given). The **Test evidence** column names the
existing test(s) that prove a `met` row; row IDs use the standard's own reference form (chapter
`V<n>`, requirement `<n>.<section>.<index>`), per ASVS *"How to Reference ASVS Requirements"*.

---

## V1 Encoding and Sanitization

| ID | Requirement (paraphrased) | L | Status | Control / file | Test evidence |
|----|---------------------------|---|--------|----------------|----------------|
| 1.1.1 | Decode/unescape untrusted input into a canonical form exactly once, only when that encoding is expected, and before any further processing (never after validation) | 2 | met | JSON body decoded once via strict decoder (`internal/userlog/userlog_handler.go`); opaque cursor base64url-decoded once then strictly re-validated (`internal/userlog/userlog_cursor.go`) | `TestDecodeJSONBodyStrict`, `TestDecodeCursor_Malformed` |
| 1.1.2 | Perform output encoding/escaping as the final step before the target interpreter (or let the interpreter do it) | 2 | met | Responses encoded at write time by `json.NewEncoder(...).Encode` (`internal/shared/response.go`) | `TestLogHandler` |
| 1.2.1 | Context-correct output encoding for HTTP/HTML/XML responses | 1 | met | JSON-only API; `json.Encoder` default HTML escaping + `application/json` envelope (`internal/shared/response.go`) | `TestLogHandler` |
| 1.2.2 | Context-encode untrusted data when building URLs; allow only safe protocols (no `javascript:`/`data:`) | 1 | n/a | No server-side URL construction from untrusted input; pagination tokens are opaque base64url cursors (`internal/userlog/userlog_cursor.go`) | — |
| 1.2.3 | Encode/escape when dynamically building JavaScript or JSON to prevent JSON injection | 1 | met | Output built structurally via `json.Encoder`, never string concatenation (`internal/shared/response.go`) | `TestLogHandler` |
| 1.2.4 | Use parameterized queries / ORM to prevent SQL and other DB injection | 1 | met | All queries are `$1..$N` parameterized constants, no string-built SQL (`internal/userlog/userlog_repository.go`) | `TestLogRepository_InjectionContent_RoundTrips_Integration` |
| 1.2.5 | Protect against OS command injection | 1 | n/a | Template makes no OS calls (no `os/exec`) | — |
| 1.2.6 | Protect against LDAP injection | 2 | n/a | No LDAP client in template | — |
| 1.2.7 | Protect against XPath injection | 2 | n/a | No XML/XPath processing | — |
| 1.2.8 | Configure LaTeX processors securely | 2 | n/a | No LaTeX processing | — |
| 1.2.9 | Escape regex metacharacters in untrusted input | 2 | n/a | Only regex is a compile-time constant, never built from user input (`internal/config/config.go`) | — |
| 1.3.1 | Sanitize untrusted HTML with a vetted library | 1 | n/a | No HTML input accepted or rendered (JSON API) | — |
| 1.3.2 | Avoid `eval()`/dynamic code execution | 1 | n/a | Go has no `eval`; no dynamic code execution | — |
| 1.3.3 | Sanitize before a dangerous context — allow only safe characters, trim over-long input | 2 | met | Over-long input rejected by rune limits (`internal/shared/validation.go`, `shared.LogMaxChars`); null bytes stripped for Postgres TEXT (`shared.SanitiseNullBytes`) | `TestService_createLog_RejectsOverLength`, `TestRuneMaxValidator` |
| 1.3.4 | Validate/sanitize user-supplied SVG | 2 | n/a | No SVG handling | — |
| 1.3.5 | Sanitize/disable user-supplied scriptable/template content (Markdown, CSS, XSL, BBCode) | 2 | n/a | No such content processed | — |
| 1.3.6 | Protect against SSRF (allowlist protocols/domains/paths/ports, sanitize before outbound) | 2 | n/a | No user-controlled outbound requests; `internal/httpclient` (template surface) is not driven by untrusted input | — |
| 1.3.7 | Prevent template injection | 2 | n/a | No server-side templating engine (JSON API) | — |
| 1.3.8 | Sanitize before JNDI queries; configure JNDI securely | 2 | n/a | No JNDI (Go) | — |
| 1.3.9 | Sanitize content before sending to memcache | 2 | n/a | No memcache client | — |
| 1.3.10 | Sanitize format strings that could resolve maliciously | 2 | met-untested | Format strings are package constants; user data is always a `%w`/`%s` argument (`internal/userlog/userlog_repository.go`, security.md rule 7) | **none (untested)** |
| 1.3.11 | Sanitize input before mail systems (SMTP/IMAP injection) | 2 | n/a | No mail integration | — |
| 1.4.1 | Use memory-safe string/copy and pointer arithmetic | 2 | n/a | Go is memory-safe; no unmanaged code or pointer arithmetic | — |
| 1.4.2 | Use sign/range/input validation to prevent integer overflows | 2 | met | Strict int parsing + explicit `[0, MaxInt32]` bounds before the pgxpool int32 conversion (`internal/db/db.go` `toInt32`, `internal/config/config.go`) | `TestToInt32`, `TestLoadDatabase_PoolSizeBounds` |
| 1.4.3 | Release memory/resources; null out freed references | 2 | n/a | Go is garbage-collected; no manual free | — |
| 1.5.1 | Configure XML parsers to disable external entities (XXE) | 1 | n/a | No XML parsing (`encoding/xml` unused) | — |
| 1.5.2 | Safe deserialization of untrusted data (type allowlist) | 2 | met | Only deserializer is `encoding/json` into typed structs with strict decoding (`internal/userlog/userlog_handler.go`) | `TestDecodeJSONBodyStrict` |

## V2 Validation and Business Logic

| ID | Requirement (paraphrased) | L | Status | Control / file | Test evidence |
|----|---------------------------|---|--------|----------------|----------------|
| 2.1.1 | Documentation defines input-validation rules (structure/format per data item) | 1 | met | Documented in `api/v1/openapi.yaml` + `.claude/rules/security.md`; enforced by validator tags (`internal/shared/validation.go`) | `TestOpenAPIPaginationMatchesSharedConstants`, `TestOpenAPILogMaxLengthMatchesSharedConstant`, `TestOpenAPIBatchMaxMatchesSharedConstant` |
| 2.1.2 | Documentation defines checks for logical/contextual consistency of combined data items | 2 | partial | Only combined-data rule is the date range (`normaliseDateRange`, `internal/userlog/userlog_service.go`); no general cross-field consistency doc — adopters extend per feature | — |
| 2.1.3 | Business-logic limits/validations documented, per-user and global | 2 | met | Single-sourced limits: batch cap `MaxBatchSize`, page size/offset (`internal/shared/limits.go`, `internal/shared/validation.go`), rate-limit model (`decisions.md` #4) | `TestOpenAPIBatchMaxMatchesSharedConstant`, `TestModelTagsDoNotHardcodeLengthLimit` |
| 2.2.1 | Validate all input against allowlists/patterns/ranges/expected structure | 1 | met | UUID `uuid.Parse`, rune-length tags, RFC3339 date parse, pagination bounds, strict JSON decode (`internal/userlog/userlog_handler.go`, `internal/shared/validation.go`) | `TestLogHandler`, `TestPathUUIDCanonicalised`, `TestService_getLogs_RejectsMalformedDate` |
| 2.2.2 | Enforce validation at a trusted server-side layer | 1 | met | All validation runs server-side in handler/service | `TestLogHandler`, `TestService_createLog_RejectsOverLength` |
| 2.2.3 | Ensure combinations of related data items are reasonable | 2 | partial | Date-range bounds validated (`normaliseDateRange`); no broader cross-field rules in the reference feature — adopter-extended | — |
| 2.3.1 | Process business-logic flow only in the expected step order | 1 | n/a | Stateless CRUD template; no multi-step sequential workflows | — |
| 2.3.2 | Enforce documented business-logic limits | 2 | met | Batch cap, rune limits, pagination bounds enforced (`internal/userlog/userlog_service.go`, `internal/shared/limits.go`) | `TestBatchCreate_OverLimitRejectedBeforeDB`, `TestValidatePagination_ClampsToMax` |
| 2.3.3 | Use transactions so a business operation fully succeeds or rolls back | 2 | met | Atomic batch create via `WithTransaction`; any row failure rolls back the lot (`internal/userlog/userlog_service.go` `createLogs`) | `TestLogService_BatchCreate_AtomicCommit_Integration`, `TestLogService_BatchCreate_RollsBackOnDBError_Integration`, `TestBatchCreate_FailingInsertRollsBack` |
| 2.3.4 | Business-level locking to stop double-booking of limited resources | 2 | n/a | No limited-quantity/reservable resources in the template | — |
| 2.4.1 | Anti-automation controls against excessive calls (exfiltration, quota exhaustion, DoS) | 2 | partial | App-level per-instance IP limiter is opt-in and default-off (`RATE_LIMIT_RPM=0`); primary control is edge per `decisions.md` #4 (`cmd/api/main.go`) — adopter must enable/edge-enforce | — |

## V3 Web Frontend Security

This is a JSON API with no HTML frontend, so most of V3 is n/a. The **security-response-headers**
subset does apply and gets rows.

| ID | Requirement (paraphrased) | L | Status | Control / file | Test evidence |
|----|---------------------------|---|--------|----------------|----------------|
| V3.1/3.2/3.3/3.5/3.6/3.7 (rest) | Browser/HTML-rendering, cookie, and client-asset controls | — | n/a | JSON API, no HTML frontend; token-auth (deferred #13), no cookies → no CSRF surface | — |
| 3.4.1 | Send Strict-Transport-Security on all responses (max-age ≥ 1yr) | 1 | gap | No HSTS header set; TLS/HSTS terminated at edge, not in-app (edge-first, cf. `decisions.md` #4) — `internal/middleware/security_middleware.go` | — |
| 3.4.2 | `Access-Control-Allow-Origin` a fixed value or validated allowlist; no sensitive data under `*` | 1 | met | Origin echoed only if in the configured allowlist, validated at startup; never wildcard-reflected — `internal/middleware/cors_middleware.go` | `TestCORS_AllowedOriginReflected`, `TestCORS_RejectedOriginNotReflected`, `TestLoad_CORSValidation` |
| 3.4.3 | Global CSP including at minimum `object-src 'none'` and `base-uri 'none'` | 2 | partial | CSP `default-src 'none'; frame-ancestors 'none'` set (`object-src` inherits `default-src 'none'`), but `base-uri` is not set explicitly — `internal/middleware/security_middleware.go` | — |
| 3.4.4 | Send `X-Content-Type-Options: nosniff` on all responses | 2 | met | Set on every response — `internal/middleware/security_middleware.go` | `TestSecurityHeaders`, `TestSecurityHeaders_AppliedRegardlessOfStatus` |
| 3.4.5 | Set a Referrer-Policy to prevent Referer leakage | 2 | met | `Referrer-Policy: no-referrer` — `internal/middleware/security_middleware.go` | `TestSecurityHeaders` |
| 3.4.6 | Use CSP `frame-ancestors` to control embedding | 2 | met | `frame-ancestors 'none'` in the CSP — `internal/middleware/security_middleware.go` | `TestSecurityHeaders` |

## V4 API and Web Service

| ID | Requirement (paraphrased) | L | Status | Control / file | Test evidence |
|----|---------------------------|---|--------|----------------|----------------|
| 4.1.1 | Every response body carries a Content-Type matching the actual content | 1 | met | `Content-Type: application/json` set per response (JSON is UTF-8) — `internal/shared/response.go`, `internal/userlog/userlog_handler.go` | `TestLogHandler`, `TestPublicRouter_MethodNotAllowedEnvelope` |
| 4.1.2 | Only user-facing browser endpoints auto-redirect HTTP→HTTPS; others must not | 2 | met-untested | App performs no transparent HTTP→HTTPS redirect; TLS edge-terminated (`decisions.md` #4) — `cmd/api/main.go` | **none (untested)** |
| 4.1.3 | Intermediary-set headers (X-Forwarded-*, X-Real-IP) cannot be overridden by the user | 2 | met | Proxy headers untrusted by default; `chimw.RealIP` opt-in via `TRUST_PROXY_HEADERS` (default false); user ID read only from server-set context — `cmd/api/main.go`, `internal/config/config.go`, `internal/shared/identity.go` | `TestLoad_TrustProxyHeaders` |
| 4.2.1 | Determine HTTP message boundaries correctly (TE vs Content-Length) to prevent smuggling | 2 | partial | Go `net/http` rejects conflicting TE/CL at the app layer; edge components are infra, out of template scope — `cmd/api/main.go` | — |
| 4.3.1 | GraphQL DoS protection (depth/amount/cost limiting) | 2 | n/a | No GraphQL | — |
| 4.3.2 | Disable GraphQL introspection in production | 2 | n/a | No GraphQL | — |
| 4.4.1 | Use WSS for all WebSocket connections | 1 | n/a | No WebSockets | — |
| 4.4.2 | Validate Origin during the WebSocket handshake | 2 | n/a | No WebSockets | — |
| 4.4.3 | WebSocket dedicated tokens comply with session-management requirements | 2 | n/a | No WebSockets | — |
| 4.4.4 | WebSocket tokens obtained/validated via the prior authenticated HTTPS session | 2 | n/a | No WebSockets | — |

## V8 Authorization

| ID | Requirement (paraphrased) | L | Status | Control / file | Test evidence |
|----|---------------------------|---|--------|----------------|----------------|
| 8.1.1 | Docs define function-level and data-specific access rules based on permissions/attributes | 1 | met | BOTH layers documented: function-level role rules in the OPA policy (`deploy/opa/policy.rego`, `decisions.md` #17) and data-specific row-ownership rules in `security.md` rule 5 / `decisions.md` #10 | `policy_test.rego` (allow/deny cases) |
| 8.1.2 | Docs define field-level (read/write) access restrictions | 2 | n/a | No field-level authorization; OPA authorises at function level, not per field (`decisions.md` #17) | — |
| 8.2.1 | Function-level access restricted to consumers with explicit permissions | 1 | met | OPA sidecar authorises `{subject, roles, action, resource}` fail-closed before the handler runs (`internal/middleware/authz_middleware.go`, `deploy/opa/policy.rego`) | `TestOPAAuth_FailsClosed`, `TestOPAAuth_AllowsOnlyOnExplicitTrue`, `policy_test.rego` |
| 8.2.2 | Data-specific access restricted per-consumer (mitigate IDOR/BOLA) | 1 | met | Every query is `WHERE … AND user_id = $N` — `internal/userlog/userlog_repository.go`; identity boundary at `internal/shared/identity.go` (UUID-enforced, 401 on bad subject) | `TestLogRepository_CrossUserIsolation_Integration`, `TestHandler_NonUUIDUserReturns401`, `TestUserIDFromContext_RejectsNonUUID` |
| 8.2.3 | Field-level access restricted per-consumer (mitigate BOPLA) | 2 | n/a | No per-field authorization; deferred with auth (`decisions.md` #13) | — |
| 8.3.1 | Enforce authorization at a trusted service layer, not client-manipulable controls | 1 | met | Row-ownership enforced server-side in the repository SQL, not the handler — `internal/userlog/userlog_repository.go`; `security.md` rule 5 | `TestLogRepository_CrossUserIsolation_Integration` |
| 8.4.1 | Multi-tenant cross-tenant controls so operations never affect other tenants | 2 | met | Per-user isolation is the tenancy boundary: all reads/writes scoped by `user_id`; forged cursors stay within the caller's rows (`decisions.md` #3) | `TestLogRepository_CrossUserIsolation_Integration` |

## V11 Cryptography

The template implements no application-level cryptography (no auth, no passwords, no in-app
encryption or key management); UUIDs come from Postgres core `gen_random_uuid()`, not used as
secrets. These become applicable when auth/secrets/PII arrive (`decisions.md` #13).

| ID | Requirement (paraphrased) | L | Status | Control / file | Test evidence |
|----|---------------------------|---|--------|----------------|----------------|
| 11.1.1 | Documented key-management policy and lifecycle | 2 | n/a | No application-managed keys; secrets via env/config (`internal/config/config.go`), key material arrives with auth (`decisions.md` #13) | — |
| 11.1.2 | Maintained cryptographic inventory | 2 | n/a | No app-level crypto to inventory; TLS terminated at edge | — |
| 11.2.1 | Use industry-validated cryptographic implementations | 2 | n/a | No app-level crypto operations | — |
| 11.2.2 | Design for crypto agility | 2 | n/a | No crypto primitives in template | — |
| 11.2.3 | All crypto primitives provide ≥128-bit security | 2 | n/a | No app-level crypto primitives | — |
| 11.3.1 | Do not use insecure block modes (ECB) or weak padding | 1 | n/a | No in-app encryption | — |
| 11.3.2 | Use only approved ciphers/modes (e.g. AES-GCM) | 1 | n/a | No in-app encryption | — |
| 11.3.3 | Protect encrypted data against tampering (AEAD/MAC) | 2 | n/a | No in-app encryption; opaque cursors are intentionally unsigned and user_id-scoped, not a confidentiality control (`decisions.md` #3) | — |
| 11.4.1 | Use only approved hash functions for cryptographic use | 1 | n/a | No cryptographic hashing in app code | — |
| 11.4.2 | Store passwords with an approved computationally-intensive KDF | 2 | n/a | No passwords/credential storage; deferred with auth (`decisions.md` #13) | — |
| 11.4.3 | Signature hashes collision-resistant, ≥256-bit | 2 | n/a | No in-app signing; release-artifact signing (SLSA) is pipeline-side, not runtime | — |
| 11.4.4 | Use approved KDFs with key-stretching for password-derived keys | 2 | n/a | No password-derived keys; deferred with auth (`decisions.md` #13) | — |
| 11.5.1 | Non-guessable random values from a CSPRNG, ≥128 bits entropy | 2 | n/a | App generates no secrets/tokens; IDs are Postgres `gen_random_uuid()` (`migrations/00001_initial_schema.sql`), not secrets | — |
| 11.6.1 | Approved algorithms for key generation, seeding, signature gen/verify | 2 | n/a | No public-key cryptography in app; deferred with auth (`decisions.md` #13) | — |

## V12 Secure Communication

TLS is terminated at the edge/LB/gateway (edge-first, the same philosophy as rate limiting —
`decisions.md` #4). The app serves plain HTTP, so TLS requirements are `partial`: satisfied in a
correct deployment, not enforced by the app.

| ID | Requirement (paraphrased) | L | Status | Control / file | Test evidence |
|----|---------------------------|---|--------|----------------|----------------|
| 12.1.1 | Only current TLS versions (1.2/1.3), latest preferred | 1 | partial | TLS terminated at edge, not in-app; app serves plain HTTP (`decisions.md` #4, README "Deployment requirements") | — |
| 12.1.2 | Only recommended cipher suites, strongest preferred | 2 | partial | Cipher policy is the LB/gateway's, not in-app | — |
| 12.1.3 | Validate mTLS client certs before trusting the identity | 2 | n/a | App does no cert-based client auth; identity via IdP/OIDC seam (`decisions.md` #13, `internal/shared/identity.go`) | — |
| 12.2.1 | TLS for all client↔external HTTP, no insecure fallback | 1 | partial | TLS terminated at edge; public `:PORT` is plain HTTP (`cmd/api/main.go`) | — |
| 12.2.2 | External-facing services use publicly trusted certs | 1 | partial | Certificate provisioning is an infra concern (edge) | — |
| 12.3.1 | TLS on all inbound/outbound connections (DB, tools, APIs) | 2 | partial | Outbound uses Go stdlib client with default cert validation (`internal/httpclient/httpclient.go`); inbound TLS + DB `sslmode` are connection-string/edge driven (`internal/db/db.go`) | — |
| 12.3.2 | TLS clients validate received certificates | 2 | partial | `httpclient` uses net/http default transport (validates by default); DB cert validation depends on `sslmode` in the DSN, not code-enforced | — |
| 12.3.3 | TLS between internal HTTP services, no insecure fallback | 2 | partial | TLS terminated at edge/mesh; internal admin `:METRICS_PORT` is plain HTTP restricted at infra (`decisions.md` #5). OTLP telemetry export to the Collector is plain HTTP in dev and expected to run over a trusted network or mesh mTLS in production (`decisions.md` #15) | — |
| 12.3.4 | Internal TLS uses trusted / pinned internal CAs | 2 | n/a | No in-app internal TLS trust store; an infra-layer concern (`decisions.md` #5) | — |

## V13 Configuration

| ID | Requirement (paraphrased) | L | Status | Control / file | Test evidence |
|----|---------------------------|---|--------|----------------|----------------|
| 13.1.1 | All application communication needs are documented | 2 | partial | README "Deployment requirements" + `httpclient` doc-comment describe outbound seam and edge dependencies; no single consolidated comms inventory | — |
| 13.2.1 | Backend comms authenticated via service accounts/short-term tokens/certs, not static shared creds | 2 | gap | DB auth uses a static password in the DSN (`internal/config/config.go`, `internal/db/db.go`); no short-term-token/cert auth | — |
| 13.2.2 | Backend accounts have least-necessary privileges | 2 | gap | DB role privileges are a deployment concern; template does not provision/constrain them | — |
| 13.2.3 | Service credential is not a default credential | 2 | partial | Secrets via env, documented in `.env.example` with non-default placeholders; enforcing non-default values is the deployer's responsibility | — |
| 13.2.4 | Allowlist of external resources the app may contact | 2 | gap | No outbound-destination allowlist; `httpclient` will call any URL passed to it | — |
| 13.2.5 | Server allowlist of resources it can request/load from | 2 | gap | No app/server-layer egress allowlist; expected at infra (firewall/egress policy), not in code | — |
| 13.3.1 | Secrets managed via a vault-style solution; never in source/artifacts | 2 | partial | Secrets read from env only (config-only `os.Getenv`), `.env*` gitignored, gitleaks scans for leaks; no key-vault integration shipped | — |
| 13.3.2 | Access to secret assets follows least privilege | 2 | gap | Secret-store access control is a deployment/infra concern; not addressed by the template | — |
| 13.4.1 | Deployed without accessible `.git`/`.svn` metadata | 1 | met-untested | App serves no filesystem; `.git`/`.env*` gitignored and excluded from the container build; API-only router (`.gitignore`, `cmd/api/main.go`) | **none (untested)** |
| 13.4.2 | Debug modes disabled in production | 2 | met-untested | Default `LOG_LEVEL=production` (fail-safe); no pprof/expvar/debug endpoints (`internal/config/config.go`, `cmd/api/main.go`) | **none (untested)** |
| 13.4.3 | No directory listings served | 2 | met-untested | JSON API only; no static file server mounted (`cmd/api/main.go`) | **none (untested)** |
| 13.4.4 | HTTP TRACE unsupported in production | 2 | met | chi registers no TRACE handler; unrouted methods return the JSON `method_not_allowed` envelope (`cmd/api/main.go`, `internal/shared`) | `TestPublicRouter_MethodNotAllowedEnvelope` |
| 13.4.5 | Docs/monitoring endpoints not exposed unless intended | 2 | met-untested | The app serves NO metrics scrape endpoint: metrics leave via OTLP push to the Collector, so there is no `/metrics` on either listener. The internal admin `:METRICS_PORT` serves only the mirrored health probes, restricted at infra (`cmd/api/main.go`, `decisions.md` #5, #15) | **none (untested)** |

## V14 Data Protection

| ID | Requirement (paraphrased) | L | Status | Control / file | Test evidence |
|----|---------------------------|---|--------|----------------|----------------|
| 14.1.1 | Identify and classify all sensitive data into protection levels | 2 | gap | No documented data classification; template stores only a `user_id` UUID + log text, and any adopter-added PII is unclassified | — |
| 14.1.2 | Each protection level has documented protection requirements | 2 | gap | No data-protection-requirements document exists for the template | — |
| 14.2.1 | Sensitive data only in HTTP body/headers; no API keys/session tokens in URL/query | 1 | met | Identity comes from request context, not the URL (`internal/shared/identity.go`); query params are only pagination/cursor/filter values (`internal/userlog/userlog_handler.go`) | `TestUserIDFromContext_RejectsNonUUID`, `TestHandler_NonUUIDUserReturns401` |
| 14.2.2 | Prevent caching of sensitive data in server components, or purge securely | 2 | met | `Cache-Control: no-store` on all responses (`internal/middleware/security_middleware.go`); readiness cache holds only health booleans (`internal/db/health_cache.go`) | `TestSecurityHeaders` |
| 14.2.3 | Defined sensitive data not sent to untrusted parties (trackers) | 2 | met-untested | Backend-only JSON API with no analytics/tracker integrations and no outbound data sharing | **none (untested)** |
| 14.2.4 | Encryption/integrity/retention/logging/access/PET controls implemented per the protection-level docs | 2 | gap | Cannot be met while 14.1.1/14.1.2 docs are absent; no defined controls to implement against | — |
| 14.3.1 | Authenticated data cleared from client storage after session end | 1 | n/a | No client/browser — backend REST API only | — |
| 14.3.2 | Sufficient anti-caching headers (`Cache-Control: no-store`) | 2 | met | Emitted on every response (`internal/middleware/security_middleware.go`) | `TestSecurityHeaders` |
| 14.3.3 | Browser storage holds no sensitive data except session tokens | 2 | n/a | No frontend/browser storage — server-side JSON API, no cookies | — |

## V15 Secure Coding and Architecture

| ID | Requirement (paraphrased) | L | Status | Control / file | Test evidence |
|----|---------------------------|---|--------|----------------|----------------|
| 15.1.1 | Documentation defines risk-based remediation timeframes for vulnerable components | 1 | gap | `govulncheck` runs in CI but no documented remediation SLA / update-timeframe policy exists | — |
| 15.1.2 | Maintain an SBOM of third-party libraries from trusted, maintained sources | 2 | met-untested | GoReleaser emits per-artifact SBOMs + checksums (`.goreleaser.yaml`); deps pinned in `go.mod`/`go.sum` | **none (untested)** |
| 15.1.3 | Documentation identifies resource-demanding functionality and availability strategy | 2 | partial | Bounded-work controls exist and are documented (request timeout, `WriteTimeout > RequestTimeout`, body cap, pagination caps — `internal/config/config.go`, `decisions.md` #11), but no explicit heavy-operations catalogue | — |
| 15.2.1 | Application contains only components within documented remediation timeframes | 1 | partial | `govulncheck` gates known CVEs and deps are pinned, but with no documented timeframe (15.1.1) compliance can't be fully asserted | — |
| 15.2.2 | Implemented defenses against availability loss from resource-demanding functionality | 2 | met | Request timeout + `WriteTimeout > RequestTimeout` validation, `MaxBytesReader`/413 body cap, bounded pagination, opt-in per-IP limiter (`internal/config/config.go`, `internal/userlog/userlog_handler.go`) | `TestRequestBodyTooLarge`, `TestLoad_FailsOnInvertedTimeouts`, `TestValidatePagination_ClampsToMax` |
| 15.2.3 | Production build exposes only required functionality (no test/sample/dev code) | 2 | met-untested | `_test.go` excluded by Go; `make deadcode` reachability; template-surface markers; no debug/sample endpoints (`cmd/api/main.go`) | **none (untested)** |
| 15.3.1 | Return only the required subset of fields, not the whole object | 1 | met | Explicit response models + every read row re-validated via `Model.Validate()` (`internal/userlog/userlog_model.go`, `internal/shared/response.go`) | `TestLogHandler` |
| 15.3.2 | Backend calls to external URLs do not follow redirects unless intended | 2 | n/a | Template makes no outbound external calls; `internal/httpclient` is unused template surface (`decisions.md` #7) | — |
| 15.3.3 | Countermeasures against mass assignment — limit allowed fields per action | 2 | met | Strict JSON decoding with `DisallowUnknownFields()`; `user_id` from auth context, never the request body (`internal/userlog/userlog_handler.go`) | `TestDecodeJSONBodyStrict` |
| 15.3.4 | Original user IP transferred via trusted, non-spoofable fields for logging/security | 2 | met | `chimw.RealIP` opt-in via `TRUST_PROXY_HEADERS` (default false); runs before the request logger so `RemoteAddr` is authoritative (`cmd/api/main.go`, `internal/config/config.go`, `internal/middleware/request_logger.go`) | `TestLoad_TrustProxyHeaders` |
| 15.3.5 | Ensure correct variable types; strict equality to avoid type juggling | 2 | met | Go is statically typed; UUID via `uuid.Parse`, strict-int env parsing, typed `Config` (`internal/config/config.go`, `internal/shared/identity.go`) | `TestGetEnvInt_FailsOnInvalid`, `TestPathUUIDCanonicalised`, `TestToInt32` |
| 15.3.6 | JavaScript written to prevent prototype pollution | 2 | n/a | No JavaScript — Go backend | — |
| 15.3.7 | Defenses against HTTP parameter pollution | 2 | met | Typed single-value query parsing + strict `DisallowUnknownFields` body decode; no ambiguous multi-source merging (`internal/userlog/userlog_handler.go`) | `TestDecodeJSONBodyStrict`, `TestListLogs_CursorOffsetMutualExclusion` |

## V16 Security Logging and Error Handling

| ID | Requirement (paraphrased) | L | Status | Control / file | Test evidence |
|----|---------------------------|---|--------|----------------|----------------|
| 16.1.1 | Maintain an inventory of what is logged per layer (format, storage, access, retention) | 2 | partial | Logging approach documented in `CLAUDE.md` + `internal/shared/logger/logger.go`, but no formal inventory of storage/access/retention (deployment/infra) | — |
| 16.2.1 | Each log entry carries investigation metadata (when/where/who/what) | 2 | met | `internal/middleware/request_logger.go` binds request_id, method, path, ip (+ user_id post-auth) + status/duration; timestamp + service/version/commit in `internal/shared/logger/logger.go` | `TestRequestLogger_BindsRequestContextAndLogsCompletion`, `TestSlogLogger_WithRequestContext_BindsAllAttrsOnEveryLine` |
| 16.2.2 | Time sources synchronized; timestamps UTC or explicit offset | 2 | partial | RFC3339 timestamps carry explicit tz offset (`internal/shared/logger/logger.go`); host clock/NTP is deployment/infra | — |
| 16.2.3 | App stores/broadcasts logs only to inventoried files/services | 2 | n/a | App writes structured JSON to stdout; collection/routing owned by edge/infra | — |
| 16.2.4 | Logs readable and correlatable, ideally a common format | 2 | met | `slog.NewJSONHandler` emits structured JSON with a request_id correlation field (`internal/shared/logger/logger.go`, `internal/middleware/request_logger.go`) | `TestSlogLogger_BaseAttrsOnEveryLine`, `TestRequestLogger_BindsRequestContextAndLogsCompletion` |
| 16.2.5 | Enforce logging rules by data-protection level (never log credentials; mask tokens) | 2 | partial | The auth middleware deliberately never logs the bearer token — a verification failure logs the error at debug WITHOUT the token (`internal/middleware/auth_middleware.go`); user text bounded by `shared.LogMaxChars` and cleaned via `shared.SanitiseNullBytes`, but no formal masking framework for other sensitive fields | — |
| 16.3.1 | Log all authentication operations, successful and failed | 2 | partial | Failed token verification is logged at debug (without the token) by the auth middleware (`internal/middleware/auth_middleware.go`); successful authentication is not separately logged (the completed-request line carries the request), and there is no dedicated audit stream | — |
| 16.3.2 | Log failed authorization attempts | 2 | partial | An OPA deny logs a WARN "authorisation denied" with the bounded resource + action (`internal/middleware/authz_middleware.go`); row-ownership denials still surface as empty/not-found through `handleError` with no distinct security event | — |
| 16.3.3 | Log defined security events / control-bypass attempts | 2 | partial | Validation/decode failures and rate-limit hits flow through `handleError` (counted on `api_errors_total`, logged), but recorded as generic errors, not tagged security-bypass events (`internal/userlog/userlog_handler.go`) | — |
| 16.3.4 | Log unexpected errors and security-control failures | 2 | met | `handleError` logs the real cause and increments `api_errors_total{feature,error_type}`; DB failures wrapped multi-`%w` (`internal/userlog/userlog_handler.go`, `internal/shared/logger/logger.go`) | `TestRepositoryPreservesUnderlyingError`, `TestHandleError_EveryErrTypeHasACase` |
| 16.4.1 | All logging components encode data to prevent log injection | 2 | met-untested | `slog.NewJSONHandler` JSON-escapes all string values; `shared.SanitiseNullBytes` strips NUL (`internal/shared/logger/logger.go`, `internal/shared/validation.go`) | **none (untested)** |
| 16.4.2 | Logs protected from unauthorized access/modification | 2 | n/a | App emits to stdout; storage integrity/access control belongs to the log pipeline | — |
| 16.4.3 | Logs securely transmitted to a logically separate system | 2 | n/a | Log shipping to a separate SIEM/store is an edge/infra concern | — |
| 16.5.1 | Generic message on unexpected/sensitive errors; no stack traces/queries/keys leaked | 2 | met | Single JSON envelope via `shared.WriteJSONError` with bounded `ErrType` constants, never `err.Error()` to the client (`internal/shared/response.go`, `internal/userlog/userlog_handler.go`) | `TestPublicRouter_NotFoundEnvelope`, `TestHandleError_EveryErrTypeHasACase`, `TestRepositoryPreservesUnderlyingError` |
| 16.5.2 | Continue operating securely when an external resource fails (graceful degradation) | 2 | partial | Readiness degrades gracefully via the cached, cancellation-detached DB ping (`internal/db/health_cache.go`); DB failures map to 500/503; no explicit circuit breaker on outbound calls | — |
| 16.5.3 | Fail gracefully and securely; no fail-open | 2 | met | Strict validation + strict decode reject before processing; output re-validated; `chimw.Recoverer` turns panics into 500 fail-closed (`cmd/api/main.go`, `internal/userlog/userlog_handler.go`) | `TestDecodeJSONBodyStrict`, `TestMiddleware_RecordsPanickingRequests` |

---

## V9 Self-contained Tokens

The template is an OAuth2 **resource server**: it validates the bearer access token it is handed
(`internal/middleware/auth_middleware.go`, via `coreos/go-oidc`). It does not ISSUE tokens — issuance
is the adopter IdP's. These rows cover the validation side; each control is cited by ID in the
middleware source.

| ID | Requirement (paraphrased) | L | Status | Control / file | Test evidence |
|----|---------------------------|---|--------|----------------|----------------|
| 9.1.1 | Validate the token's signature/MAC before trusting its contents | 1 | met | go-oidc verifies the JWT signature against the discovered JWKS before any claim is read (`internal/middleware/auth_middleware.go`) | `TestOIDCAuth_RejectsWith401` (bad signature) |
| 9.1.2 | Algorithm allowlist for token verification; never `None` | 1 | met | Explicit asymmetric-only allowlist `authAllowedSigningAlgs` (RS256, ES256); `none` is never on it (`internal/middleware/auth_middleware.go`) | `TestOIDCAuth_RejectsWith401` (none alg) |
| 9.1.3 | Key material only from trusted pre-configured sources; reject token-supplied `jku`/`x5u`/`jwk` | 1 | met | go-oidc's `RemoteKeySet` fetches keys only from the discovery `jwks_uri`; token header key sources are never honoured (`internal/middleware/auth_middleware.go`) | `TestOIDCAuth_RejectsWith401` (bad signature — a key outside the trusted set cannot verify) |
| 9.2.1 | Accept the token only within its validity window (`exp`/`nbf`) | 1 | met | go-oidc rejects expired/not-yet-valid tokens (`internal/middleware/auth_middleware.go`) | `TestOIDCAuth_RejectsWith401` (expired) |
| 9.2.2 | Validate token TYPE / purpose; only accept access tokens for authorization | 2 | met | `token_use=="id"` rejected; documented assumption for issuers without a token-use claim (`internal/middleware/auth_middleware.go`) | `TestOIDCAuth_RejectsIDToken` |
| 9.2.3 | Accept only tokens whose audience is this service (`aud`) | 2 | met | Verifier `ClientID` = `OIDC_AUDIENCE`; go-oidc validates `aud` (`internal/middleware/auth_middleware.go`, `internal/config/config.go`) | `TestOIDCAuth_RejectsWith401` (wrong audience) |
| 9.2.4 | If the issuer reuses a key across audiences, tokens carry a unique audience restriction | 2 | n/a | Token-ISSUER responsibility (adopter IdP), not the resource server; the RS validates `aud` at 9.2.3 | — |

---

## V10 OAuth and OIDC

The resource-server requirements (**V10.3**, and the identity parts of **V10.5**) are `met` with the
middleware and its tests. The authorization-SERVER requirements (**V10.4**, **V10.6**), the
browser/login-flow client requirements (**V10.1**, **V10.2**, **V10.5.1** nonce, **V10.5.5**
back-channel logout), and **V10.7** consent are `n/a`: the template does not run the login flow (see
`decisions.md` #16/#19). No row here claims an authorization-server control the template does not
implement.

| ID | Requirement (paraphrased) | L | Status | Control / file | Test evidence |
|----|---------------------------|---|--------|----------------|----------------|
| 10.1.1 | Tokens sent only to components that need them (e.g. BFF keeps access/refresh in the backend) | 2 | n/a | OIDC client/frontend responsibility; the RS receives tokens, it does not distribute them. Browser forks warned off `localStorage` (README) | — |
| 10.1.2 | Client accepts AS values only from a same-session transaction (PKCE `code_verifier`, `state`, `nonce`) | 2 | n/a | Login-flow (OIDC client) responsibility; the RS runs no authorization request | — |
| 10.2.1 | OAuth client CSRF defence via PKCE or `state` | 2 | n/a | OAuth client responsibility (login flow); not run by the RS | — |
| 10.2.2 | OAuth client mix-up defence (validate `iss`) | 2 | n/a | OAuth client responsibility (login flow); not run by the RS | — |
| 10.2.3 | OAuth client requests only the required scopes | 3 | n/a | L3 (above the L2 bar) and an OAuth client responsibility | — |
| 10.3.1 | Resource server accepts only tokens intended for it (audience) | 2 | met | Verifier `ClientID` = `OIDC_AUDIENCE` (`internal/middleware/auth_middleware.go`) | `TestOIDCAuth_RejectsWith401` (wrong audience) |
| 10.3.2 | Resource server enforces authorization from token claims (`sub`, `scope`, …) | 2 | met | Subject + roles (mapped from claims) form the OPA input; the decision is enforced fail-closed (`internal/middleware/authz_middleware.go`) | `TestOPAAuth_BuildsBoundedInput`, `TestOPAAuth_FailsClosed` |
| 10.3.3 | Identify the user from non-reassignable claims (typically `iss`+`sub`) | 2 | met | `MapSubjectToUUID(iss, sub)` — never a mutable claim like email (`internal/middleware/auth_middleware.go`) | `TestMapSubjectToUUID`, `TestOIDCAuth_ValidToken_SetsMappedUUID` |
| 10.3.4 | If specific auth strength/recentness is required, verify `acr`/`amr`/`auth_time` | 2 | n/a | The template imposes no step-up / auth-strength requirement; add `acr`/`amr`/`auth_time` checks in the middleware if a deployment needs one | — |
| 10.3.5 | Prevent stolen/replayed access tokens via sender-constrained tokens (mTLS/DPoP) | 3 | n/a | L3 (above the L2 bar); sender-constrained tokens are a deployment/IdP choice | — |
| 10.4.1–10.4.16 | OAuth Authorization Server controls (redirect-URI allowlist, code single-use, PKCE required, refresh rotation, client auth, …) | 1–3 | n/a | Authorization SERVER (adopter IdP) responsibility; the template is a resource server (`decisions.md` #16/#19) | — |
| 10.5.1 | OIDC client mitigates ID-token replay via `nonce` | 2 | n/a | Login-flow (OIDC client) responsibility; the RS runs no authentication request, so there is no `nonce` to bind | — |
| 10.5.2 | Uniquely identify the user from a non-reassignable `sub` | 2 | met | `MapSubjectToUUID(iss, sub)` derives a stable internal UUID (`internal/middleware/auth_middleware.go`) | `TestMapSubjectToUUID` |
| 10.5.3 | Reject AS metadata whose issuer does not EXACTLY match the pre-configured issuer | 2 | met | `oidc.NewProvider` enforces exact issuer match; `oidc.InsecureIssuerURLContext` is deliberately not used (`internal/middleware/auth_middleware.go`) | `TestOIDCAuth_DiscoveryIssuerMismatchRejected` |
| 10.5.4 | Validate the token audience equals this client's id | 2 | met | Same `aud`-vs-`OIDC_AUDIENCE` check as 10.3.1 (`internal/middleware/auth_middleware.go`) | `TestOIDCAuth_RejectsWith401` (wrong audience) |
| 10.5.5 | OIDC back-channel logout: validate `logout+jwt` typing, `events`, no `nonce` | 2 | n/a | Back-channel logout not implemented; logout is a client/IdP flow, not a resource-server surface | — |
| 10.6.1–10.6.2 | OpenID Provider controls (response modes, forced-logout DoS) | 2 | n/a | OpenID PROVIDER (adopter IdP) responsibility; the template is not an OP | — |
| 10.7.1–10.7.3 | Consent management (prompt, information, review/revoke) | 2 | n/a | Authorization server / consent-UI responsibility; not a resource-server surface | — |

---

## Chapter-level (no per-requirement rows)

| Chapter | Level of assessment | Reason |
|---------|---------------------|--------|
| V5 File Handling | n/a | No file-upload endpoint exists, so file-handling controls (security.md rule 6: MIME + extension + size) have no surface — becomes applicable when an adopter adds an upload endpoint, and this map gains rows then. |
| V6 Authentication | partial (IdP-owned) | The app is a RESOURCE SERVER (`decisions.md` #13/#16): it VALIDATES bearer access tokens (see V9 and V10.3 below) but runs no login, credential store, MFA, or recovery flow — those are the adopter IdP's. Password/MFA/recovery requirements have no surface here; the token-validation slice is covered by the per-requirement V9 and V10.3 rows. |
| V7 Session Management | n/a (IdP-owned, stateless RS) | The resource server holds no session: each request is authorised solely by its bearer token (`internal/middleware/auth_middleware.go`), there is no cookie, session store, or logout. Session lifecycle is the authorization server / client responsibility. |
| V17 WebRTC | n/a | The template exposes only a JSON REST API with no real-time/WebRTC media, so no control has any surface. |

---

## Gaps (findings)

Every `gap`, `partial`, and `met-untested` row is a finding. **Not fixed in this round** (except the
V8 isolation gap — see the changelog) — the repository owner decides which become future tasks.
`gap` = no template control; `partial` = satisfied outside the app (edge/deployment) or only in
part; `met-untested` = the control exists in code but **no test proves it**, so it can regress
undetected.

### met-untested (met in code, no test proves it — write a test or accept the risk)

These behaviours are implemented but unverified. Most are architectural or deployment properties
that a unit test cannot easily assert (no redirect, no debug endpoint, pipeline-produced SBOM); each
is a candidate for a targeted test or an explicit accept-the-risk decision.

| ID | Area | Behaviour with no test |
|----|------|------------------------|
| 1.3.10 | V1 | Format strings are package constants (user data is always an argument) — no test asserts a user-controlled format string is impossible. |
| 4.1.2 | V4 | App performs no transparent HTTP→HTTPS redirect — no test asserts the absence. |
| 13.4.1 | V13 | No filesystem / `.git` served — no test. |
| 13.4.2 | V13 | Debug modes off (`LOG_LEVEL=production`, no pprof/expvar) — no test asserts no debug endpoint is registered. |
| 13.4.3 | V13 | No directory listings — no test. |
| 13.4.5 | V13 | App serves no metrics scrape endpoint (metrics push via OTLP). No test asserts `/metrics` is absent from both listeners. |
| 14.2.3 | V14 | No data sent to third-party trackers (no such integration) — nothing to test. |
| 15.1.2 | V15 | SBOM is produced by the GoReleaser pipeline, not a Go test. |
| 15.2.3 | V15 | Prod build excludes test/sample code (`make deadcode` is a target, not a `go test`). |
| 16.4.1 | V16 | Log injection prevented by the JSON handler's escaping — no test injects control characters into a log field and asserts escaping. |

### Gaps (no template control — schedule a decision)

| ID | Area | What's missing |
|----|------|----------------|
| 3.4.1 | V3 headers | No in-app HSTS header (TLS/HSTS is edge-terminated by design — could add HSTS in-app as defence-in-depth, or document the edge requirement). |
| 13.2.1 | V13 config | Backend (DB) auth is a static password in the DSN; no service-account / short-term-token / cert auth. |
| 13.2.2 | V13 config | DB role least-privilege is not provisioned or constrained by the template. |
| 13.2.4 | V13 config | No allowlist of external resources the app may contact. |
| 13.2.5 | V13 config | No app/server-layer egress allowlist (expected at infra, not in code). |
| 13.3.2 | V13 config | Secret-store least-privilege access control is unaddressed (deployment concern). |
| 14.1.1 | V14 data | No documented data classification of sensitive data. |
| 14.1.2 | V14 data | No documented protection requirements per data class. |
| 14.2.4 | V14 data | Protection controls can't be asserted until 14.1.1/14.1.2 docs exist. |
| 15.1.1 | V15 arch | No documented risk-based remediation timeframe for vulnerable dependencies. |

### Partials (satisfied at the edge / in part — verify per deployment)

| ID | Area | Note |
|----|------|------|
| 2.1.2, 2.2.3 | V2 | Cross-field consistency only covers the date range in the reference feature; adopters extend per feature. |
| 2.4.1 | V2 | App limiter is opt-in/default-off; primary anti-automation is the edge (`decisions.md` #4). |
| 3.4.3 | V3 headers | CSP omits an explicit `base-uri 'none'` (object-src inherits `default-src 'none'`). |
| 4.2.1 | V4 | Request-smuggling defence is app-layer (Go `net/http`) only; edge boundary handling is infra. |
| 8.1.1 | V8 | Only data-specific (row-ownership) authz rules are documented; function-level/role rules arrive with auth. |
| 12.1.1–12.3.3 | V12 | TLS terminated at the edge, not enforced in-app (`decisions.md` #4). |
| 13.1.1, 13.2.3, 13.3.1 | V13 | Comms inventory / non-default-credential enforcement / vault integration are partial or deployer-owned. |
| 15.1.3, 15.2.1 | V15 | Availability-strategy doc and remediation-timeframe compliance depend on the missing 15.1.1 policy. |
| 16.1.1, 16.2.2, 16.2.5, 16.3.2, 16.3.3, 16.5.2 | V16 | Log inventory/retention, clock sync, masking framework, distinct authz-denied/security-bypass events, and circuit-breaking are deployment-owned or arrive with auth. |
