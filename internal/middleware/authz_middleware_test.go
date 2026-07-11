package middleware

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// TestOPAAuth_ReusesConnection proves the decision body is drained so the OPA
// connection is returned to the keep-alive pool: two sequential decisions must
// reuse ONE TCP connection, not dial a fresh one each time. Without the drain
// (json.Decoder stops at the end of the JSON value and leaves the rest unread),
// the Transport can't reuse the connection and each authorized request re-dials
// the sidecar on the hot path.
//
// The stub appends >2KB of trailing bytes after the decision object. Go's
// Transport auto-slurps only a small remainder (~2KB) on Close, so this models
// the real chunked-encoding case where the decoder leaves the chunk terminator
// unread: only the explicit io.Copy(io.Discard) drain reaches EOF and preserves
// keep-alive. With a tiny body the auto-slurp would hide the bug.
func TestOPAAuth_ReusesConnection(t *testing.T) {
	var mu sync.Mutex
	newConns := 0
	trailing := strings.Repeat(" ", 4096) // exceeds the Transport's on-Close auto-slurp limit
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"result": true}`+trailing)
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			mu.Lock()
			newConns++
			mu.Unlock()
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)

	authz := opaAuthorizerFor(srv.URL, time.Second)
	input := opaInput{Subject: "11111111-1111-1111-1111-111111111111", Action: "read", Resource: "/api/v1/logs"}
	for range 2 {
		allowed, err := authz.decide(context.Background(), input)
		if err != nil || !allowed {
			t.Fatalf("decide: allowed=%v err=%v", allowed, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if newConns != 1 {
		t.Errorf("opened %d new connections for 2 sequential decisions; want 1 (body not drained → keep-alive broken)", newConns)
	}
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
// internal subject UUID, the mapped action, the method, and the caller's roles.
//
// NOTE on the resource field: this test drives the middleware in ISOLATION (no
// router), so chi has resolved NO route pattern and input.resource is the honest
// "unknown" sentinel. The real, load-bearing resource assertion — that OPA
// receives the LEAF pattern, not the mount "/api/v1/*" — lives in
// TestOPAAuth_ResourceIsLeafPattern_RealRouter, which assembles the router the
// way cmd/api/main.go does. An earlier version of this test hand-populated
// rctx.RoutePatterns to fake a resolved pattern; that masked the mount-pattern
// bug because production never populates it at middleware time.
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
	// Carry an authenticated subject + roles as the auth middleware would have set
	// them. Deliberately NO chi route context is injected: see the note above.
	ctx := shared.WithUserID(context.Background(), uid)
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
	if got.Resource != "unknown" {
		t.Errorf("resource without a resolved router: got %q, want %q (leaf-pattern resolution is covered by the real-router test)", got.Resource, "unknown")
	}
	if got.Method != http.MethodDelete {
		t.Errorf("method: got %q, want %q", got.Method, http.MethodDelete)
	}
	if len(got.Roles) != 1 || got.Roles[0] != "admin" {
		t.Errorf("roles: got %v, want [admin]", got.Roles)
	}
}

// TestOPAAuth_ResourceIsLeafPattern_RealRouter is the decisive regression guard
// for the mount-pattern bug (fix #1). It assembles the /api/v1 router EXACTLY as
// cmd/api/main.go does — via middleware.AuthorizedRoutes, so authz runs as an
// inline-group (post-routing) middleware — points OPA at a capturing stub, and
// asserts that input.resource is the resolved chi LEAF pattern ("/api/v1/logs",
// "/api/v1/logs/{id}"), NEVER the mount pattern "/api/v1/*". Before the fix, a
// plain r.Use(authz) on the subrouter sent "/api/v1/*" and every request 403'd.
func TestOPAAuth_ResourceIsLeafPattern_RealRouter(t *testing.T) {
	var lastResource string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req opaRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		lastResource = req.Input.Resource
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"result": true}`) // allow so the request reaches the handler
	}))
	t.Cleanup(srv.Close)
	authz := opaAuthorizerFor(srv.URL, time.Second)

	// Stub authn injects an authenticated subject exactly as the OIDC middleware
	// would (pre-routing), so authz sees a real caller.
	const uid = "11111111-1111-1111-1111-111111111111"
	stubAuthn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(shared.WithUserID(r.Context(), uid)))
		})
	}
	handlerHit := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

	router := chi.NewRouter()
	router.Route("/api/v1", func(r chi.Router) {
		AuthorizedRoutes(r, stubAuthn, authz.Handler, func(r chi.Router) {
			r.Get("/logs", handlerHit)
			r.Get("/logs/{id}", handlerHit)
		})
	})

	cases := []struct {
		path         string
		wantResource string
	}{
		{path: "/api/v1/logs", wantResource: "/api/v1/logs"},
		{path: "/api/v1/logs/abc123", wantResource: "/api/v1/logs/{id}"},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			lastResource = ""
			req := httptest.NewRequest(http.MethodGet, c.path, nil)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("expected the allow to reach the handler (200), got %d", rr.Code)
			}
			if lastResource == "/api/v1/*" {
				t.Fatalf("OPA received the MOUNT pattern %q — the mount-pattern bug is back", lastResource)
			}
			if lastResource != c.wantResource {
				t.Errorf("input.resource: got %q, want the leaf pattern %q", lastResource, c.wantResource)
			}
		})
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
