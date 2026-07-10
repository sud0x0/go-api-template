package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"

	"github.com/sud0x0/go-api-template/internal/config"
	"github.com/sud0x0/go-api-template/internal/shared"
	"github.com/sud0x0/go-api-template/internal/shared/logger"
)

// This file implements LAYER 1 of the two-layer auth model: AUTHENTICATION.
//
// The template is an OAuth2 RESOURCE SERVER. It receives a Bearer access token
// in the Authorization header, validates it against the IdP's discovered JWKS,
// and maps the caller's identity to the internal UUID via shared.WithUserID.
// Layer 2 (authorisation) is the OPA middleware in authz_middleware.go.
//
// OUT OF SCOPE (deliberately, not forgotten, see .claude/rules/decisions.md and
// the ASVS map V10): the login redirect, authorization-code exchange, PKCE, the
// `state`/`nonce` parameters, redirect-URI registration, refresh-token rotation,
// consent, and back-channel logout. Those are authorization-SERVER and browser-
// CLIENT concerns owned by the adopter's IdP and frontend. A resource server
// only validates the token it is handed; it never runs the flow that mints it.

// authAllowedSigningAlgs is the explicit signature-algorithm allowlist for token
// verification (ASVS V9.1.2). It is ASYMMETRIC-ONLY (RSA + ECDSA): symmetric
// algorithms (HS*) are excluded to avoid the key-confusion attack where a token
// signed with HMAC using the public key as the secret is accepted, and the
// 'none' algorithm is never present. go-oidc rejects any token whose `alg` is
// not on this list before the signature is even checked.
//
// It is a package var (not baked into the verifier config inline) so the tests
// verify against the exact same allowlist the production verifier uses.
var authAllowedSigningAlgs = []string{oidc.RS256, oidc.ES256}

// subjectNamespace is the fixed UUIDv5 namespace used to derive an internal
// UUID from an external (issuer, subject) pair. It is a constant so the mapping
// is deterministic and reproducible across restarts and replicas with no lookup
// table. Changing it would re-map every existing user to a new UUID, so it is
// frozen. Treat it like a schema constant.
var subjectNamespace = uuid.MustParse("a1e0f7d2-3b4c-4d5e-8f60-71829a3b4c5d")

// tokenVerifier is the subset of *oidc.IDTokenVerifier the middleware needs.
// Depending on the interface (not the concrete type) lets the tests inject a
// verifier built from a StaticKeySet via oidc.NewVerifier, so no network or live
// IdP is required to exercise every accept/reject path.
type tokenVerifier interface {
	Verify(ctx context.Context, rawIDToken string) (*oidc.IDToken, error)
}

// OIDCAuthenticator is the resource-server authentication middleware. Build it
// ONCE at startup with NewOIDCAuthenticator (which performs OIDC discovery and
// caches the provider + verifier); the per-request Handler then does no
// discovery and no key fetch on the hot path. go-oidc's RemoteKeySet fetches
// and rotates the JWKS in the background.
type OIDCAuthenticator struct {
	verifier tokenVerifier
	log      logger.Logger
}

// NewOIDCAuthenticator performs OIDC discovery against cfg.IssuerURL and returns
// an authenticator whose verifier is configured to:
//
//   - V9.1.1: verify the token's signature (go-oidc's KeySet does this against
//     the discovered JWKS before any claim is trusted).
//   - V9.1.2: accept ONLY algorithms on authAllowedSigningAlgs; the 'none'
//     algorithm is never on the list, so an unsigned token is rejected.
//   - V9.1.3: take key material ONLY from the JWKS discovered at the trusted
//     issuer. go-oidc's RemoteKeySet fetches keys from the discovery document's
//     jwks_uri and NEVER honours a token-supplied `jku`, `x5u`, or `jwk` header,
//     so an attacker cannot point verification at a key they control.
//   - V9.2.1: reject tokens outside their exp/nbf validity window.
//   - V9.2.3 / V10.3.1: validate the `aud` claim against cfg.Audience (passed
//     as the verifier's ClientID).
//   - V10.5.3: require the discovered issuer to match cfg.IssuerURL EXACTLY.
//     oidc.NewProvider enforces this and we deliberately do NOT use
//     oidc.InsecureIssuerURLContext (which relaxes it for off-spec providers
//     such as Azure); relaxing issuer validation is an insecure opt-in we do not
//     expose. If an adopter genuinely needs it, they add it here as a documented
//     insecure flag. It is absent by design.
//
// Discovery makes a network call, so pass a context with a sensible startup
// timeout. A discovery failure is fatal: the app must not boot with auth
// "enabled" but no verifier (that would fail open).
func NewOIDCAuthenticator(ctx context.Context, cfg config.OIDCConfig, log logger.Logger) (*OIDCAuthenticator, error) {
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, err
	}
	verifier := provider.Verifier(&oidc.Config{
		ClientID:             cfg.Audience,
		SupportedSigningAlgs: authAllowedSigningAlgs,
	})
	return &OIDCAuthenticator{verifier: verifier, log: log}, nil
}

// tokenClaims captures only the claims the middleware inspects beyond what
// go-oidc validates structurally. token_use is how some IdPs (e.g. AWS Cognito)
// mark whether a token is an access or id token; see the V9.2.2 note in Handler.
// Nonce/AtHash/CHash are ID-token-ONLY claims used to positively reject an ID
// token presented as an access token. Roles/Groups feed the vendor-agnostic role
// boundary (see mapClaimsToRoles).
type tokenClaims struct {
	TokenUse string   `json:"token_use"`
	Roles    []string `json:"roles"`
	Groups   []string `json:"groups"`
	// nonce, at_hash and c_hash are defined by OIDC Core (§2, §3.1.3.6, §3.3.2.11)
	// exclusively for ID tokens: an access token never carries them. Their
	// presence therefore POSITIVELY identifies an ID token regardless of IdP.
	Nonce  string `json:"nonce"`
	AtHash string `json:"at_hash"`
	CHash  string `json:"c_hash"`
}

// rolesContextKey is the package-private context key carrying the caller's
// internal roles from the authentication middleware (which sets them) to the
// authorisation middleware (which reads them into the OPA input). It is
// unexported and private to internal/middleware because roles are an
// infrastructure detail shared only between these two middlewares. Feature
// packages never read them (they authorise by user_id in SQL). The key is NOT
// in internal/shared because, unlike the user ID, no feature package needs it.
type rolesContextKey struct{}

// withRoles returns a copy of ctx carrying the caller's internal roles.
func withRoles(ctx context.Context, roles []string) context.Context {
	return context.WithValue(ctx, rolesContextKey{}, roles)
}

// rolesFromContext returns the internal roles set by the authentication
// middleware, or nil if none were set (auth disabled, or the token carried no
// role/group claims). The OPA middleware passes these through as input.roles.
func rolesFromContext(ctx context.Context) []string {
	roles, _ := ctx.Value(rolesContextKey{}).([]string)
	return roles
}

// mapClaimsToRoles is the VENDOR-AGNOSTIC role boundary. IdP tokens carry
// vendor-specific role/group claims; the OPA policy is written against INTERNAL
// role names. Mapping here, at the authentication boundary, means the IdP and
// the policy are independently swappable: change IdPs and you only re-point this
// function, the policy is untouched.
//
// DENY BY DEFAULT: the shipped template maps EVERY token to an EMPTY internal
// role set. This is deliberate. The starter policy (deploy/opa/policy.rego)
// grants global cross-user access to anyone whose role set contains the literal
// "admin"; a verbatim passthrough of the token's roles ∪ groups would hand that
// privilege to any caller whose IdP token merely carries a group named "admin"
// (a name the adopter does not control and an attacker may be able to obtain).
// So the default trusts NO IdP group name as an internal role. Self-access still
// works (the policy's self rule is role-independent); only elevated/cross-user
// access is withheld until the adopter opts in below.
//
// TO ENABLE ROLES: populate the `trusted` allowlist below with the SPECIFIC IdP
// group/role names you control, mapped to internal role names (e.g.
// "eng-admins" → "admin"). Only names in the allowlist become internal roles;
// everything else is dropped. This is the safe inverse of the old verbatim
// passthrough (roles ∪ groups), which trusted arbitrary IdP group names and so
// handed "admin" to anyone whose token merely carried a group called "admin".
// mapClaimsToRoles is exercised by the middleware tests.
func mapClaimsToRoles(roles, groups []string) []string {
	// The trusted mapping from IdP group/role names to INTERNAL role names.
	// EMPTY by default → deny by default: no token grants any internal role until
	// an adopter curates this map. Example: map[string]string{
	// "eng-admins": "admin", "team-leads": "team-lead"}.
	return mapClaimsToRolesWith(map[string]string{}, roles, groups)
}

// mapClaimsToRolesWith translates the token's IdP role/group names to internal
// role names using an EXPLICIT trusted allowlist: only a name present in
// `trusted` becomes its mapped internal role; every other name is dropped. The
// result is de-duplicated, preserving roles-then-groups order. It is split out
// from mapClaimsToRoles so the mapping mechanism is unit-testable with a curated
// allowlist without any mutable global state (the shipped default passes an empty
// map, which denies by default).
func mapClaimsToRolesWith(trusted map[string]string, roles, groups []string) []string {
	seen := make(map[string]struct{}, len(roles)+len(groups))
	var out []string
	for _, name := range append(append([]string{}, roles...), groups...) {
		internal, ok := trusted[name]
		if !ok {
			continue // untrusted IdP name → not an internal role
		}
		if _, dup := seen[internal]; dup {
			continue
		}
		seen[internal] = struct{}{}
		out = append(out, internal)
	}
	return out
}

// Handler is the net/http middleware. On every request it extracts and verifies
// the Bearer access token, then stores the caller's internal UUID on the request
// context via shared.WithUserID so downstream handlers and repositories are
// user-scoped. Any failure short-circuits with a 401 in the JSON envelope and a
// `WWW-Authenticate: Bearer` header. The request never reaches a feature
// handler unauthenticated.
func (a *OIDCAuthenticator) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			shared.WriteUnauthorised(w, shared.ErrTypeUnauthorised, "missing or malformed bearer token")
			return
		}

		idToken, err := a.verifier.Verify(r.Context(), raw)
		if err != nil {
			// Log at debug WITHOUT the token: an invalid-token line at info would
			// be a log-flood and DoS-log vector on unauthenticated traffic, and the
			// token itself is a credential that must never be logged.
			logger.FromContext(r.Context(), a.log).LogDebug("token verification failed", "error", err.Error())
			shared.WriteUnauthorised(w, shared.ErrTypeUnauthorised, "invalid or expired token")
			return
		}

		// V9.2.2: token type. Only ACCESS tokens may drive authorisation; an ID
		// token proves authentication to a client and must not be accepted here.
		//
		// The PRIMARY guard is the audience check (V9.2.3): OIDC_AUDIENCE must be
		// this API's own resource identifier that the IdP stamps into ACCESS tokens'
		// `aud`, distinct from any browser client_id. An ID token's `aud` is the
		// client_id, so it fails the audience check. (This only holds if the
		// operator followed the OIDC_AUDIENCE guidance in config.go; setting it to a
		// browser client_id re-opens the confusion.)
		//
		// Layered on top, a POSITIVE token-type assertion rejects an ID token even
		// when its audience happens to match: token_use=="id" (Cognito) OR the
		// presence of any OIDC-Core ID-token-ONLY claim (nonce / at_hash / c_hash),
		// which an access token never carries. There is no universal positive
		// access-token signal go-oidc surfaces (RFC 9068's `typ: at+jwt` JOSE header
		// is not exposed and non-9068 IdPs omit it), so we detect the ID token
		// instead of trying to prove the access token.
		var claims tokenClaims
		// A claims-decode error is non-fatal for role extraction (roles simply
		// stay empty), but the token-type gate below is enforced when present.
		_ = idToken.Claims(&claims)
		if claims.TokenUse == "id" || claims.Nonce != "" || claims.AtHash != "" || claims.CHash != "" {
			logger.FromContext(r.Context(), a.log).LogDebug("rejected id token used as access token")
			shared.WriteUnauthorised(w, shared.ErrTypeUnauthorised, "id token not accepted for authorization")
			return
		}

		// V10.3.3 / V10.5.2: identify the user from claims that cannot be
		// reassigned: the (issuer, subject) pair, never a mutable claim like email.
		// idToken.Issuer is already validated to equal the configured issuer, and
		// idToken.Subject is the IdP's stable subject id.
		if idToken.Subject == "" {
			shared.WriteUnauthorised(w, shared.ErrTypeUnauthorised, "token has no subject")
			return
		}
		internalUUID := MapSubjectToUUID(idToken.Issuer, idToken.Subject)

		// Map IdP role/group claims to internal roles at the boundary and carry
		// them forward for the OPA authorisation middleware (input.roles). The
		// policy is written against these internal roles, not IdP group names.
		ctx := shared.WithUserID(r.Context(), internalUUID)
		ctx = withRoles(ctx, mapClaimsToRoles(claims.Roles, claims.Groups))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// MapSubjectToUUID deterministically maps an external (issuer, subject) pair to
// an internal UUID.
//
// The template's user_id column is UUID NOT NULL, but an IdP `sub` is often not
// a UUID ("auth0|12345", an email, a numeric id). Rather than ship a lookup
// table, the template derives the internal id as UUIDv5(namespace, iss|sub):
// same inputs always yield the same UUID, so it is reproducible across restarts
// and replicas with no database round-trip. The issuer is included so two IdPs
// that happen to mint the same `sub` never collide onto one internal user.
//
// This is the TEMPLATE's choice for a zero-infrastructure default. A real
// deployment may instead keep a `users` table mapping (iss, sub) → an internal
// id and resolve it in this middleware before WithUserID: swap the body of this
// function for that lookup. Either way, UserIDFromContext still enforces the
// UUID shape at the single read point.
func MapSubjectToUUID(issuer, subject string) string {
	return uuid.NewSHA1(subjectNamespace, []byte(issuer+"|"+subject)).String()
}

// bearerToken extracts the token from an "Authorization: Bearer <token>" header
// value. The scheme match is case-insensitive (RFC 7235 makes it so), the token
// must be non-empty, and no second space is allowed (a bearer token is a single
// opaque string). Returns ("", false) for anything that is not exactly one
// bearer credential.
func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}
