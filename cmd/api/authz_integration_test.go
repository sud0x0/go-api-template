//go:build integration

// Integration proof for the OPA authorisation wiring (fix #1) against a REAL
// stack: real Postgres + a real OPA sidecar serving deploy/opa/policy.rego. It
// assembles the /api/v1 router EXACTLY as main() does (via
// middleware.AuthorizedRoutes) and points OPA at a capturing proxy that records
// input.resource and forwards to the real OPA, so the assertions use the SHIPPED
// policy's real decisions while proving the resource string OPA receives.
//
// Run with the dev stack up (make run) and the env pointed at it:
//
//	DB_HOST=localhost DB_PORT=5432 DB_USER=appuser DB_PASSWORD=changeme \
//	DB_NAME=appdb DB_SSLMODE=disable \
//	OPA_URL=http://localhost:8181 OPA_DECISION_PATH=v1/data/api/authz/allow \
//	  go test -tags integration ./cmd/api/ -run Authz -v
//
// Absent OPA_URL or the DB_* env, it skips (like the other integration tests).

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sud0x0/go-api-template/internal/config"
	"github.com/sud0x0/go-api-template/internal/db"
	"github.com/sud0x0/go-api-template/internal/middleware"
	"github.com/sud0x0/go-api-template/internal/shared"
	"github.com/sud0x0/go-api-template/internal/shared/logger"
	"github.com/sud0x0/go-api-template/internal/userlog"
)

type noopAuthzMetrics struct{}

func (noopAuthzMetrics) IncAPIError(_ context.Context, _, _ string) {}

func envOrSkip(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("integration test requires %s (run `make run` and export the stack env)", key)
	}
	return v
}

// TestAuthz_RealStack_Integration is the decisive end-to-end proof for fix #1.
func TestAuthz_RealStack_Integration(t *testing.T) {
	opaURL := envOrSkip(t, "OPA_URL")
	_ = envOrSkip(t, "DB_HOST") // fail fast to a skip if the DB env is absent

	log := logger.NewLogger("silent", "authz-int-test")

	// Real database + real userlog handler.
	dbCfg, err := config.LoadDatabase()
	if err != nil {
		t.Fatalf("config.LoadDatabase: %v", err)
	}
	database, err := db.New(dbCfg, log, nil, userlog.RequiredTables)
	if err != nil {
		t.Fatalf("db.New (is the stack migrated? run `make run`): %v", err)
	}
	t.Cleanup(database.Close)

	repo := userlog.NewLogRepository(database.Pool())
	svc := userlog.NewLogService(repo, database)
	logHandler := userlog.NewLogHandler(svc, log, noopAuthzMetrics{})

	// Capturing proxy in front of the REAL OPA: record input.resource, forward the
	// body verbatim, return OPA's real decision. This keeps the SHIPPED policy as
	// the decider while letting us assert the resource string.
	decisionPath := getEnvDefault("OPA_DECISION_PATH", "v1/data/api/authz/allow")
	realDecisionURL := strings.TrimRight(opaURL, "/") + "/" + strings.TrimLeft(decisionPath, "/")
	var mu sync.Mutex
	var lastResource string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Input struct {
				Resource string `json:"resource"`
			} `json:"input"`
		}
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		lastResource = req.Input.Resource
		mu.Unlock()

		resp, err := http.Post(realDecisionURL, "application/json", bytes.NewReader(body))
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(proxy.Close)

	opaAuthz := middleware.NewOPAAuthorizer(config.OPAConfig{
		URL:          proxy.URL,
		DecisionPath: decisionPath,
		Timeout:      3 * time.Second,
	}, log)

	// Stub authn injects an authenticated subject exactly as the OIDC middleware
	// would (pre-routing), with NO roles — the shipped deny-by-default mapping.
	const caller = "a1b2c3d4-1111-4111-8111-000000000001"
	stubAuthn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(shared.WithUserID(r.Context(), caller)))
		})
	}

	// Assemble the /api/v1 router EXACTLY as main() does.
	router := chi.NewRouter()
	router.Route("/api/v1", func(r chi.Router) {
		middleware.AuthorizedRoutes(r, stubAuthn, opaAuthz.Handler, logHandler.Routes)
	})

	getResource := func() string { mu.Lock(); defer mu.Unlock(); return lastResource }

	// (a) + (b): self-access list — resource is the LEAF "/api/v1/logs" (never the
	// mount "/api/v1/*"), and the shipped policy ALLOWS it (200, not the old 403).
	t.Run("self-access GET /api/v1/logs is allowed with leaf resource", func(t *testing.T) {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/logs", nil))

		if got := getResource(); got != "/api/v1/logs" {
			t.Errorf("OPA input.resource: got %q, want the leaf %q (mount-pattern bug would give %q)", got, "/api/v1/logs", "/api/v1/*")
		}
		if rr.Code != http.StatusOK {
			t.Fatalf("self-access must be allowed by the shipped policy: got %d, want 200 (body=%q)", rr.Code, rr.Body.String())
		}
	})

	// The item route resolves to the parameterised leaf pattern, and self-access is
	// allowed (a 404 for a not-owned id is fine — the point is it is NOT a 403).
	t.Run("self-access GET /api/v1/logs/{id} uses the parameterised leaf", func(t *testing.T) {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/logs/b2c3d4e5-2222-4222-8222-000000000002", nil))

		if got := getResource(); got != "/api/v1/logs/{id}" {
			t.Errorf("OPA input.resource: got %q, want the leaf %q", got, "/api/v1/logs/{id}")
		}
		if rr.Code == http.StatusForbidden {
			t.Fatalf("self-access read must not be 403 under the shipped policy (body=%q)", rr.Body.String())
		}
	})

	// (#3) A caller with the deny-by-default (empty) role set may NOT act
	// cross-user: naming another owner via ?user= is denied 403 by the policy.
	t.Run("cross-user without privilege is denied", func(t *testing.T) {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/logs?user=c3d4e5f6-3333-4333-8333-000000000003", nil))

		if rr.Code != http.StatusForbidden {
			t.Fatalf("cross-user with empty roles must be 403, got %d (body=%q)", rr.Code, rr.Body.String())
		}
	})

	// (#4) Directly ask the REAL OPA about an ADJACENT resource that merely shares
	// the "/api/v1/logs" prefix: the anchored is_logs must deny it even for admin.
	t.Run("adjacent resource is denied by the anchored policy", func(t *testing.T) {
		for _, res := range []string{"/api/v1/logsettings", "/api/v1/logs-admin"} {
			if allowed := queryOPA(t, realDecisionURL, res); allowed {
				t.Errorf("adjacent resource %q must be denied (anchored is_logs), but policy allowed it", res)
			}
		}
		// Sanity: the real logs collection IS allowed for the same admin input, so
		// the deny above is the anchoring, not a broken query.
		if allowed := queryOPA(t, realDecisionURL, "/api/v1/logs"); !allowed {
			t.Error("sanity: admin on /api/v1/logs should be allowed by the shipped policy")
		}
	})
}

// queryOPA POSTs an admin decision input for the given resource straight to the
// real OPA and returns the boolean result (false when undefined/denied).
func queryOPA(t *testing.T, decisionURL, resource string) bool {
	t.Helper()
	const admin = "11111111-1111-4111-8111-111111111111"
	body, _ := json.Marshal(map[string]any{"input": map[string]any{
		"subject":     admin,
		"roles":       []string{"admin"},
		"action":      "read",
		"resource":    resource,
		"method":      "GET",
		"target_user": admin,
	}})
	resp, err := http.Post(decisionURL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("query OPA: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Result *bool `json:"result"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Result != nil && *out.Result
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
