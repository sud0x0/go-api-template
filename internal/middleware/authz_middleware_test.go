package middleware

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sud0x0/go-api-template/internal/config"
	"github.com/sud0x0/go-api-template/internal/shared"
	"github.com/sud0x0/go-api-template/internal/shared/logger"
)

// The OPA middleware is tested against an httptest server standing in for the
// OPA sidecar, so every fail-closed path (transport error, timeout, non-200,
// malformed body, missing result, explicit false) is exercised with no real OPA.

// opaStub returns a test server that responds with the given status and raw body
// to the decision POST.
func opaStub(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func opaAuthorizerFor(url string, timeout time.Duration) *OPAAuthorizer {
	return NewOPAAuthorizer(config.OPAConfig{
		URL:          url,
		DecisionPath: "v1/data/api/authz/allow",
		Timeout:      timeout,
	}, logger.NewLogger("silent", "test"))
}

// allowCountingNext records whether the request was allowed through.
func allowCountingNext(allowed *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*allowed = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestOPAAuth_AllowsOnlyOnExplicitTrue(t *testing.T) {
	srv := opaStub(t, http.StatusOK, `{"result": true}`)
	authz := opaAuthorizerFor(srv.URL, time.Second)

	var allowed bool
	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs", nil)
	rr := httptest.NewRecorder()
	authz.Handler(allowCountingNext(&allowed)).ServeHTTP(rr, req)

	if !allowed || rr.Code != http.StatusOK {
		t.Fatalf("result==true must allow: allowed=%v status=%d (body=%q)", allowed, rr.Code, rr.Body.String())
	}
}

// Every deny/fail-closed path yields a 403 in the "forbidden" envelope and never
// calls next.
func TestOPAAuth_FailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		slow   bool // server delays past the timeout
	}{
		{name: "explicit false", status: http.StatusOK, body: `{"result": false}`},
		{name: "missing result", status: http.StatusOK, body: `{}`},
		{name: "non-200", status: http.StatusInternalServerError, body: `{"result": true}`},
		{name: "malformed body", status: http.StatusOK, body: `{not json`},
		{name: "empty body", status: http.StatusOK, body: ``},
		{name: "timeout", status: http.StatusOK, body: `{"result": true}`, slow: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var srv *httptest.Server
			if c.slow {
				srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					time.Sleep(200 * time.Millisecond)
					w.WriteHeader(http.StatusOK)
					_, _ = io.WriteString(w, `{"result": true}`)
				}))
				t.Cleanup(srv.Close)
			} else {
				srv = opaStub(t, c.status, c.body)
			}

			timeout := time.Second
			if c.slow {
				timeout = 30 * time.Millisecond
			}
			authz := opaAuthorizerFor(srv.URL, timeout)

			var allowed bool
			req := httptest.NewRequest(http.MethodGet, "/api/v1/logs", nil)
			rr := httptest.NewRecorder()
			authz.Handler(allowCountingNext(&allowed)).ServeHTTP(rr, req)

			if allowed {
				t.Errorf("%s must NOT allow the request through", c.name)
			}
			if rr.Code != http.StatusForbidden {
				t.Fatalf("%s: got status %d, want 403 (body=%q)", c.name, rr.Code, rr.Body.String())
			}
			if env := decodeEnvelope(t, rr.Body.Bytes()); env.ErrorType != shared.ErrTypeForbidden {
				t.Errorf("%s: error type got %q, want %q", c.name, env.ErrorType, shared.ErrTypeForbidden)
			}
		})
	}
}

// A transport error (OPA unreachable) also fails closed.
func TestOPAAuth_UnreachableFailsClosed(t *testing.T) {
	// Port 1 is not listening; the client dial fails fast.
	authz := opaAuthorizerFor("http://127.0.0.1:1", time.Second)

	var allowed bool
	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs", nil)
	rr := httptest.NewRecorder()
	authz.Handler(allowCountingNext(&allowed)).ServeHTTP(rr, req)

	if allowed || rr.Code != http.StatusForbidden {
		t.Fatalf("unreachable OPA must fail closed: allowed=%v status=%d", allowed, rr.Code)
	}
}

// The decision input document carries the bounded, well-formed fields: the
// internal subject UUID, the mapped action, the chi route pattern (never the raw
// path), the method, and the caller's roles.
func TestOPAAuth_BuildsBoundedInput(t *testing.T) {
	var got opaInput
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req opaRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		got = req.Input
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"result": true}`)
	}))
	t.Cleanup(srv.Close)
	authz := opaAuthorizerFor(srv.URL, time.Second)

	const uid = "11111111-1111-1111-1111-111111111111"
	// Carry an authenticated subject + roles, and a chi route pattern, as the auth
	// middleware and router would have set them.
	rctx := chi.NewRouteContext()
	rctx.RoutePatterns = []string{"/api/v1/logs/{id}"}
	ctx := context.WithValue(context.Background(), chi.RouteCtxKey, rctx)
	ctx = shared.WithUserID(ctx, uid)
	ctx = withRoles(ctx, []string{"admin"})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/logs/abc", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	var allowed bool
	authz.Handler(allowCountingNext(&allowed)).ServeHTTP(rr, req)

	if !allowed {
		t.Fatalf("expected allow, status=%d", rr.Code)
	}
	if got.Subject != uid {
		t.Errorf("subject: got %q, want %q", got.Subject, uid)
	}
	if got.Action != "delete" {
		t.Errorf("action: got %q, want %q (DELETE maps to delete)", got.Action, "delete")
	}
	if got.Resource != "/api/v1/logs/{id}" {
		t.Errorf("resource: got %q, want the chi route pattern %q", got.Resource, "/api/v1/logs/{id}")
	}
	if got.Method != http.MethodDelete {
		t.Errorf("method: got %q, want %q", got.Method, http.MethodDelete)
	}
	if len(got.Roles) != 1 || got.Roles[0] != "admin" {
		t.Errorf("roles: got %v, want [admin]", got.Roles)
	}
}

// A `?user=<uuid>` target is derived, canonicalised, carried into the OPA input
// as target_user, and (on an allow) stored on the request context for the
// handler. This is the cross-user seam: the target is DATA the policy decides on.
func TestOPAAuth_CrossUserTargetThreadedAndAllowed(t *testing.T) {
	var got opaInput
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req opaRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		got = req.Input
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"result": true}`)
	}))
	t.Cleanup(srv.Close)
	authz := opaAuthorizerFor(srv.URL, time.Second)

	const caller = "11111111-1111-1111-1111-111111111111"
	// Upper-case (with hex letters) to prove canonicalisation to bare lowercase
	// before the value is used.
	const targetRaw = "AABBCCDD-EEFF-4A2B-8C3D-112233445566"
	const targetCanonical = "aabbccdd-eeff-4a2b-8c3d-112233445566"

	ctx := shared.WithUserID(context.Background(), caller)
	ctx = withRoles(ctx, []string{"admin"})
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/logs/abc?user="+targetRaw, nil).WithContext(ctx)
	rr := httptest.NewRecorder()

	var seenTarget string
	var seenOK bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenTarget, seenOK = shared.TargetUserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	authz.Handler(next).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected allow, got status %d (body=%q)", rr.Code, rr.Body.String())
	}
	if got.TargetUser != targetCanonical {
		t.Errorf("input.target_user: got %q, want canonical %q", got.TargetUser, targetCanonical)
	}
	if got.Subject != caller {
		t.Errorf("input.subject: got %q, want %q", got.Subject, caller)
	}
	if !seenOK || seenTarget != targetCanonical {
		t.Errorf("handler context target: got (%q, ok=%v), want %q", seenTarget, seenOK, targetCanonical)
	}
}

// With no `?user=` parameter the target defaults to the caller's own subject
// (ordinary self-access), and that is what OPA sees.
func TestOPAAuth_TargetDefaultsToSubject(t *testing.T) {
	var got opaInput
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req opaRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		got = req.Input
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"result": true}`)
	}))
	t.Cleanup(srv.Close)
	authz := opaAuthorizerFor(srv.URL, time.Second)

	const caller = "11111111-1111-1111-1111-111111111111"
	ctx := shared.WithUserID(context.Background(), caller)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	authz.Handler(allowCountingNext(new(bool))).ServeHTTP(rr, req)

	if got.TargetUser != caller {
		t.Errorf("target defaults to subject: got %q, want %q", got.TargetUser, caller)
	}
}

// A malformed `?user=` target is the caller's bad input: a 400 (invalid_target)
// BEFORE any OPA round-trip and before any query, never a 500.
func TestOPAAuth_MalformedTargetIs400(t *testing.T) {
	var opaCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		opaCalled = true
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"result": true}`)
	}))
	t.Cleanup(srv.Close)
	authz := opaAuthorizerFor(srv.URL, time.Second)

	ctx := shared.WithUserID(context.Background(), "11111111-1111-1111-1111-111111111111")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs?user=not-a-uuid", nil).WithContext(ctx)
	rr := httptest.NewRecorder()

	var reachedNext bool
	authz.Handler(allowCountingNext(&reachedNext)).ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("malformed target: got status %d, want 400 (body=%q)", rr.Code, rr.Body.String())
	}
	if env := decodeEnvelope(t, rr.Body.Bytes()); env.ErrorType != shared.ErrTypeInvalidTarget {
		t.Errorf("error type: got %q, want %q", env.ErrorType, shared.ErrTypeInvalidTarget)
	}
	if opaCalled {
		t.Error("OPA must not be consulted for a malformed target")
	}
	if reachedNext {
		t.Error("handler must not be reached for a malformed target")
	}
}

// A cross-user tuple OPA denies (explicit false) fails closed as a 403, and the
// handler is never reached: an unauthorised cross-user attempt cannot leak.
func TestOPAAuth_CrossUserDeniedFailsClosed(t *testing.T) {
	srv := opaStub(t, http.StatusOK, `{"result": false}`)
	authz := opaAuthorizerFor(srv.URL, time.Second)

	ctx := shared.WithUserID(context.Background(), "11111111-1111-1111-1111-111111111111")
	ctx = withRoles(ctx, []string{"user"}) // a plain user, not admin
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/logs/abc?user=22222222-2222-2222-2222-222222222222", nil).WithContext(ctx)
	rr := httptest.NewRecorder()

	var reachedNext bool
	authz.Handler(allowCountingNext(&reachedNext)).ServeHTTP(rr, req)

	if reachedNext {
		t.Error("cross-user deny must not reach the handler")
	}
	if rr.Code != http.StatusForbidden {
		t.Fatalf("got status %d, want 403", rr.Code)
	}
	if env := decodeEnvelope(t, rr.Body.Bytes()); env.ErrorType != shared.ErrTypeForbidden {
		t.Errorf("error type: got %q, want %q", env.ErrorType, shared.ErrTypeForbidden)
	}
}

func TestMethodToAction(t *testing.T) {
	cases := map[string]string{
		http.MethodGet:     "read",
		http.MethodHead:    "read",
		http.MethodPost:    "create",
		http.MethodPut:     "update",
		http.MethodPatch:   "update",
		http.MethodDelete:  "delete",
		http.MethodOptions: "options",
	}
	for method, want := range cases {
		if got := methodToAction(method); got != want {
			t.Errorf("methodToAction(%q) = %q, want %q", method, got, want)
		}
	}
}
