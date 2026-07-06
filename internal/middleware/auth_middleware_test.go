package middleware

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"

	"github.com/sud0x0/go-api-template/internal/config"
	"github.com/sud0x0/go-api-template/internal/shared"
	"github.com/sud0x0/go-api-template/internal/shared/logger"
)

// These tests exercise the resource-server authentication middleware with NO
// network and NO live IdP: the verifier is built from an in-memory RSA key via
// oidc.StaticKeySet + oidc.NewVerifier, exactly as go-oidc documents for
// testing. That is why OIDCAuthenticator depends on the tokenVerifier interface
// rather than the concrete *oidc.IDTokenVerifier — the production verifier and
// the test verifier are the same shape.

const (
	testIssuer   = "https://issuer.test"
	testAudience = "go-api"
)

// testSigner bundles a keypair with the audience/issuer a verifier expects.
type testSigner struct {
	priv   *rsa.PrivateKey
	signer jose.Signer
}

func newTestSigner(t *testing.T) *testSigner {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sig, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: priv},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	return &testSigner{priv: priv, signer: sig}
}

// sign serialises claims into a compact RS256 JWT.
func (s *testSigner) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	jws, err := s.signer.Sign(payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	raw, err := jws.CompactSerialize()
	if err != nil {
		t.Fatalf("serialise: %v", err)
	}
	return raw
}

// verifierFor builds a verifier that trusts pub, using the SAME algorithm
// allowlist the production authenticator uses (authAllowedSigningAlgs) so the
// none-alg rejection is a real test of the shipped config, not a test-only one.
func verifierFor(pub crypto.PublicKey) tokenVerifier {
	keySet := &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{pub}}
	return oidc.NewVerifier(testIssuer, keySet, &oidc.Config{
		ClientID:             testAudience,
		SupportedSigningAlgs: authAllowedSigningAlgs,
	})
}

func testAuthenticator(v tokenVerifier) *OIDCAuthenticator {
	return &OIDCAuthenticator{verifier: v, log: logger.NewLogger("silent", "test")}
}

// validClaims returns a well-formed access-token claim set.
func validClaims(sub string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss": testIssuer,
		"sub": sub,
		"aud": testAudience,
		"iat": now.Unix(),
		"nbf": now.Add(-time.Minute).Unix(),
		"exp": now.Add(time.Hour).Unix(),
	}
}

// capturedUserID is set by the terminal handler so a test can assert what the
// middleware placed on the context.
func capturingNext(captured *string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := shared.UserIDFromContext(r.Context())
		*captured = id
		w.WriteHeader(http.StatusOK)
	})
}

func decodeEnvelope(t *testing.T, body []byte) shared.ErrorResponse {
	t.Helper()
	var env shared.ErrorResponse
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("response is not the JSON error envelope: %v (body=%q)", err, body)
	}
	return env
}

func TestOIDCAuth_ValidToken_SetsMappedUUID(t *testing.T) {
	s := newTestSigner(t)
	auth := testAuthenticator(verifierFor(s.priv.Public()))

	var got string
	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs", nil)
	req.Header.Set("Authorization", "Bearer "+s.sign(t, validClaims("auth0|12345")))
	rr := httptest.NewRecorder()
	auth.Handler(capturingNext(&got)).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("valid token: got status %d, want 200 (body=%q)", rr.Code, rr.Body.String())
	}
	want := MapSubjectToUUID(testIssuer, "auth0|12345")
	if got != want {
		t.Errorf("context user id: got %q, want mapped UUID %q", got, want)
	}
}

// Every 401 path is table-driven: each case yields an Authorization header (or
// none) that must be rejected with a 401, the "unauthorised" envelope, and a
// WWW-Authenticate header.
func TestOIDCAuth_RejectsWith401(t *testing.T) {
	s := newTestSigner(t)     // trusted key
	other := newTestSigner(t) // untrusted key (for the bad-signature case)
	auth := testAuthenticator(verifierFor(s.priv.Public()))

	// A hand-crafted alg=none token: header {"alg":"none"}, the valid payload,
	// and an empty signature. It must be rejected because "none" is not on the
	// allowlist (ASVS V9.1.2).
	noneToken := func() string {
		b64 := func(v any) string {
			j, _ := json.Marshal(v)
			return base64.RawURLEncoding.EncodeToString(j)
		}
		return b64(map[string]any{"alg": "none", "typ": "JWT"}) + "." + b64(validClaims("nope")) + "."
	}()

	expired := validClaims("expired-user")
	expired["exp"] = time.Now().Add(-time.Hour).Unix()

	wrongAud := validClaims("aud-user")
	wrongAud["aud"] = "some-other-service"

	cases := []struct {
		name   string
		header string
	}{
		{"missing header", ""},
		{"malformed scheme", "Token abc.def.ghi"},
		{"bearer without token", "Bearer "},
		{"garbage token", "Bearer not-a-jwt"},
		{"bad signature", "Bearer " + other.sign(t, validClaims("x"))},
		{"none alg", "Bearer " + noneToken},
		{"expired", "Bearer " + s.sign(t, expired)},
		{"wrong audience", "Bearer " + s.sign(t, wrongAud)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got string
			req := httptest.NewRequest(http.MethodGet, "/api/v1/logs", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			rr := httptest.NewRecorder()
			auth.Handler(capturingNext(&got)).ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("got status %d, want 401 (body=%q)", rr.Code, rr.Body.String())
			}
			if got != "" {
				t.Errorf("rejected request must not set a user id, got %q", got)
			}
			if env := decodeEnvelope(t, rr.Body.Bytes()); env.ErrorType != shared.ErrTypeUnauthorised {
				t.Errorf("error type: got %q, want %q", env.ErrorType, shared.ErrTypeUnauthorised)
			}
			if rr.Header().Get("WWW-Authenticate") == "" {
				t.Error("401 must carry a WWW-Authenticate header")
			}
		})
	}
}

// An id token (token_use=="id") must not be accepted for authorization (V9.2.2),
// even though it is otherwise a valid, correctly-signed, correctly-audienced token.
func TestOIDCAuth_RejectsIDToken(t *testing.T) {
	s := newTestSigner(t)
	auth := testAuthenticator(verifierFor(s.priv.Public()))

	claims := validClaims("id-token-user")
	claims["token_use"] = "id"

	var got string
	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs", nil)
	req.Header.Set("Authorization", "Bearer "+s.sign(t, claims))
	rr := httptest.NewRecorder()
	auth.Handler(capturingNext(&got)).ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("id token: got status %d, want 401 (body=%q)", rr.Code, rr.Body.String())
	}
	if got != "" {
		t.Errorf("id token must not authenticate, got user id %q", got)
	}
}

// TestOIDCAuth_PropagatesRoles pins the vendor-agnostic role boundary END TO END
// through the middleware: a verified token's roles/groups claims must reach the
// authorisation layer as internal roles (via rolesFromContext), mapped exactly as
// mapClaimsToRoles defines. This is the wiring that makes the IdP swappable, so
// each case captures the context the middleware hands to next and asserts the
// roles on it. Roles are NOT required to authenticate, so every case still
// yields 200 with a user id set.
func TestOIDCAuth_PropagatesRoles(t *testing.T) {
	s := newTestSigner(t)
	auth := testAuthenticator(verifierFor(s.priv.Public()))

	cases := []struct {
		name   string
		roles  any // claim value to set, or nil to omit the claim entirely
		groups any
		want   []string
	}{
		{"roles claim propagates", []string{"admin"}, nil, []string{"admin"}},
		{"groups claim propagates", nil, []string{"eng"}, []string{"eng"}},
		{"union and de-duplication", []string{"admin"}, []string{"admin", "eng"}, []string{"admin", "eng"}},
		{"empty when both absent", nil, nil, nil},
		{"blank entries dropped", []string{"admin", ""}, []string{"", "eng"}, []string{"admin", "eng"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			claims := validClaims("role-user")
			if c.roles != nil {
				claims["roles"] = c.roles
			}
			if c.groups != nil {
				claims["groups"] = c.groups
			}

			var gotRoles []string
			var gotUser string
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotRoles = rolesFromContext(r.Context())
				gotUser, _ = shared.UserIDFromContext(r.Context())
				w.WriteHeader(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodGet, "/api/v1/logs", nil)
			req.Header.Set("Authorization", "Bearer "+s.sign(t, claims))
			rr := httptest.NewRecorder()
			auth.Handler(next).ServeHTTP(rr, req)

			// Roles never gate authentication.
			if rr.Code != http.StatusOK {
				t.Fatalf("got status %d, want 200 (body=%q)", rr.Code, rr.Body.String())
			}
			if gotUser == "" {
				t.Error("request authenticated but no user id was set on the context")
			}
			if !equalRoles(gotRoles, c.want) {
				t.Errorf("rolesFromContext: got %v, want %v", gotRoles, c.want)
			}
		})
	}
}

// TestMapClaimsToRoles is the direct, pure-function test of the mapping the
// middleware test above exercises through the wire. Order is deterministic:
// roles first, then groups, each de-duplicated, empty strings dropped.
func TestMapClaimsToRoles(t *testing.T) {
	cases := []struct {
		name   string
		roles  []string
		groups []string
		want   []string
	}{
		{"roles only", []string{"admin"}, nil, []string{"admin"}},
		{"groups only", nil, []string{"eng"}, []string{"eng"}},
		{"union preserves roles-then-groups order", []string{"admin"}, []string{"eng"}, []string{"admin", "eng"}},
		{"de-duplicates across roles and groups", []string{"admin"}, []string{"admin", "eng"}, []string{"admin", "eng"}},
		{"de-duplicates within roles", []string{"admin", "admin"}, nil, []string{"admin"}},
		{"drops blank entries", []string{"admin", ""}, []string{"", "eng"}, []string{"admin", "eng"}},
		{"both empty yields nil", nil, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mapClaimsToRoles(c.roles, c.groups)
			if !equalRoles(got, c.want) {
				t.Errorf("mapClaimsToRoles(%v, %v) = %v, want %v", c.roles, c.groups, got, c.want)
			}
		})
	}
}

// equalRoles compares role slices for order-sensitive equality; a nil and an
// empty slice both count as "no roles".
func equalRoles(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// NewOIDCAuthenticator must reject a discovery document whose issuer does not
// EXACTLY match the configured issuer (ASVS V10.5.3) — a malicious or
// mis-configured authorization server cannot impersonate another by returning a
// different issuer in its metadata. This also exercises the real discovery path.
func TestOIDCAuth_DiscoveryIssuerMismatchRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			w.Header().Set("Content-Type", "application/json")
			// issuer deliberately differs from the requested issuer (this server's
			// URL). Discovery fails on the mismatch before any JWKS fetch, so the
			// jwks_uri value is irrelevant.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":   "https://evil.example.com",
				"jwks_uri": "https://evil.example.com/keys",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	cfg := config.OIDCConfig{Enabled: true, IssuerURL: srv.URL, Audience: testAudience}
	_, err := NewOIDCAuthenticator(t.Context(), cfg, logger.NewLogger("silent", "test"))
	if err == nil {
		t.Fatal("expected discovery to reject an issuer that does not match the configured issuer")
	}
}

func TestMapSubjectToUUID(t *testing.T) {
	a := MapSubjectToUUID(testIssuer, "auth0|12345")
	aAgain := MapSubjectToUUID(testIssuer, "auth0|12345")
	b := MapSubjectToUUID(testIssuer, "auth0|99999")
	crossIssuer := MapSubjectToUUID("https://other.test", "auth0|12345")

	if a != aAgain {
		t.Errorf("mapping must be deterministic: %q != %q", a, aAgain)
	}
	if a == b {
		t.Errorf("different subjects must map to different UUIDs, both got %q", a)
	}
	if a == crossIssuer {
		t.Errorf("same subject under different issuers must not collide, both got %q", a)
	}
	// The result must be a canonical UUID that UserIDFromContext accepts (else a
	// mapped subject would surface as a 500 at query time).
	ctx := shared.WithUserID(t.Context(), a)
	if id, ok := shared.UserIDFromContext(ctx); !ok || id != a {
		t.Errorf("mapped value %q is not a canonical UUID accepted by UserIDFromContext", a)
	}
}

func TestBearerToken(t *testing.T) {
	cases := []struct {
		header string
		want   string
		wantOK bool
	}{
		{"Bearer abc", "abc", true},
		{"bearer abc", "abc", true}, // scheme is case-insensitive
		{"BEARER abc", "abc", true},
		{"", "", false},           // missing
		{"Bearer", "", false},     // scheme only
		{"Bearer ", "", false},    // empty token
		{"Basic abc", "", false},  // wrong scheme
		{"Bearer a b", "", false}, // two tokens
	}
	for _, c := range cases {
		got, ok := bearerToken(c.header)
		if got != c.want || ok != c.wantOK {
			t.Errorf("bearerToken(%q) = (%q, %v), want (%q, %v)", c.header, got, ok, c.want, c.wantOK)
		}
	}
}
