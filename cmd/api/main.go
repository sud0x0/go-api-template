// Command api is the go-api-template HTTP server entrypoint. It wires
// configuration, database, telemetry, logging, and middleware, then serves the
// public API on :PORT and the internal admin listener (health only) on
// :METRICS_PORT. Metrics and traces are pushed via OTLP to a Collector, not
// scraped from the app. See the README "Quick start" to run it.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	metricnoop "go.opentelemetry.io/otel/metric/noop"

	"github.com/sud0x0/go-api-template/internal/config"
	"github.com/sud0x0/go-api-template/internal/db"
	"github.com/sud0x0/go-api-template/internal/metrics"
	"github.com/sud0x0/go-api-template/internal/middleware"
	"github.com/sud0x0/go-api-template/internal/observability"
	"github.com/sud0x0/go-api-template/internal/shared"
	"github.com/sud0x0/go-api-template/internal/shared/logger"
	"github.com/sud0x0/go-api-template/internal/userlog"
)

// readinessCacheTTL bounds how long a readiness result is reused. Short enough
// that a genuine DB outage is reflected within ~2s, long enough that a probe
// flood collapses to at most one DB ping per window. The cached ping runs on a
// context detached from the caller's cancellation (see db.HealthCache.Check), so
// a client that disconnects mid-ping cannot poison the result shared with the
// real load-balancer probe.
const readinessCacheTTL = 2 * time.Second

// registerEnvelopeFallbacks installs router-level 404/405 handlers that emit the
// shared JSON error envelope, replacing chi's plain-text defaults. Use it on the
// PUBLIC router only. The internal admin router keeps chi defaults.
//
// These are router-level errors (no feature label) and are deliberately NOT
// recorded on api.errors. http.server.request.count already counts them via the
// metrics middleware.
//
// chi does NOT hand the allowed-method list to a custom MethodNotAllowed handler
// (it is unexported on the route context), so RFC 9110 §15.5.6's requirement
// that a 405 carry an `Allow` header would be dropped. We recompute it by walking
// the router for the request path across the standard method set and listing the
// ones that match. WriteJSONError does not touch the Allow header, so the value
// set here survives into the JSON-envelope 405.
func registerEnvelopeFallbacks(r chi.Router) {
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		shared.WriteJSONError(w, http.StatusNotFound, shared.ErrTypeNotFound, "resource not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		if allow := allowedMethodsFor(r, req.URL.Path); allow != "" {
			w.Header().Set("Allow", allow)
		}
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, shared.ErrTypeMethodNotAllowed, "method not allowed")
	})
}

// probeMethods is the fixed set of HTTP methods allowedMethodsFor walks the
// router with. It is the RFC 9110 method registry plus PATCH (RFC 5789) — the
// same bounded set the metrics layer normalises against.
var probeMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodConnect,
	http.MethodOptions, http.MethodTrace,
}

// allowedMethodsFor returns the comma-separated list of methods the router has a
// route for at path (the RFC 9110 `Allow` header value), or "" if none. It uses
// chi's read-only Match to probe each method without invoking any handler or
// middleware, so it reflects exactly the registered routes.
func allowedMethodsFor(router chi.Router, path string) string {
	var allowed []string
	for _, m := range probeMethods {
		if router.Match(chi.NewRouteContext(), m, path) {
			allowed = append(allowed, m)
		}
	}
	return strings.Join(allowed, ", ")
}

func main() {
	// Parse command-line flags. --version / -v / --healthcheck short-circuit
	// before any config, DB, or server initialisation.
	if shouldExit, code := handleFlags(os.Args[1:], os.Stdout); shouldExit {
		os.Exit(code)
	}

	// Load configuration from environment variables.
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	// Initialise logger with configured level. "go-api" is bound onto every
	// log line as the service attribute, alongside the build's version and
	// commit. See internal/shared/logger.NewLogger.
	appLogger := logger.NewLogger(cfg.Log.Level, "go-api")
	// Install the app logger as slog's process-wide default so any code logging
	// via the slog package default routes through our handler. Done here (not in
	// the constructor) so constructing a logger never mutates global state.
	logger.SetDefaultSlog(appLogger)
	appLogger.LogInfo("starting application")

	// Initialise OpenTelemetry providers (traces + metrics over OTLP) BEFORE
	// metrics.New so its instruments bind to the global MeterProvider this
	// installs. When telemetry is disabled (no OTEL_EXPORTER_OTLP_ENDPOINT) the
	// globals stay no-op and the app runs without a Collector.
	otelShutdown, otelEnabled, err := observability.Init(context.Background(), cfg.Observability)
	if err != nil {
		appLogger.LogError(errors.New("observability init failed"), err)
		os.Exit(1)
	}
	if otelEnabled {
		appLogger.LogInfo("telemetry enabled",
			"otlp_endpoint", cfg.Observability.OTELExporterEndpoint,
			"service_name", cfg.Observability.OTELServiceName,
		)
	} else {
		appLogger.LogInfo("telemetry disabled: OTEL_EXPORTER_OTLP_ENDPOINT is unset, " +
			"traces and metrics are no-ops; logs still emit to stdout")
	}

	// Initialise metrics AFTER the MeterProvider is installed so the pgx pool
	// can be created with the query tracer wired in.
	appMetrics := metrics.New()

	// Each feature exports the tables it needs; main aggregates them so the
	// startup schema check never couples internal/db to a feature package. Add
	// a new feature's RequiredTables to this Concat call.
	requiredTables := slices.Concat(
		userlog.RequiredTables,
	)

	// Initialise database with the query tracer. Every query on this pool
	// records into db.client.operation.duration and db.client.operation.errors
	// (with pgx.ErrNoRows excluded from the error counter).
	database, err := db.New(&cfg.Database, appLogger, appMetrics.QueryTracer(), requiredTables)
	if err != nil {
		appLogger.LogError(err, nil)
		os.Exit(1)
	}

	// Register the pool-stats async callback. It reads pool.Stat() once at each
	// metric collection, with no goroutine or background polling.
	if err := appMetrics.RegisterPoolCollector(database.Pool()); err != nil {
		appLogger.LogError(errors.New("pool metrics registration failed"), err)
	}

	// Two-layer auth model (see .claude/rules/decisions.md): LAYER 1
	// authentication (OIDC, in-process) then LAYER 2 authorisation (OPA sidecar
	// over REST). Both are opt-in. When a layer is disabled its middleware is nil
	// and simply not registered, so the template boots with no IdP and no OPA for
	// local dev. Constructed BEFORE the router so an OIDC discovery failure aborts
	// startup rather than booting with auth "enabled" but no verifier (which would
	// fail open). The disabled paths log a WARN so an unauthenticated deployment is
	// never a silent surprise.
	var oidcAuth *middleware.OIDCAuthenticator
	if cfg.Auth.OIDC.Enabled {
		discoveryCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		oidcAuth, err = middleware.NewOIDCAuthenticator(discoveryCtx, cfg.Auth.OIDC, appLogger)
		cancel()
		if err != nil {
			appLogger.LogError(errors.New("oidc discovery failed"), err)
			os.Exit(1)
		}
		appLogger.LogInfo("authentication enabled (OIDC)",
			"issuer", cfg.Auth.OIDC.IssuerURL, "audience", cfg.Auth.OIDC.Audience)
	} else {
		appLogger.LogWarn("authentication DISABLED: OIDC_ISSUER_URL is unset. " +
			"/api/v1 runs unauthenticated (local-dev mode; set OIDC_ISSUER_URL to enable)")
	}

	var opaAuthz *middleware.OPAAuthorizer
	if cfg.Auth.OPA.Enabled {
		opaAuthz = middleware.NewOPAAuthorizer(cfg.Auth.OPA, appLogger)
		appLogger.LogInfo("authorisation enabled (OPA)",
			"opa_url", cfg.Auth.OPA.URL, "decision_path", cfg.Auth.OPA.DecisionPath)
	} else {
		appLogger.LogWarn("authorisation DISABLED: OPA_URL is unset. " +
			"/api/v1 requests are not policy-checked (local-dev mode; set OPA_URL to enable)")
	}

	// Build the public API router (:PORT).
	publicRouter := chi.NewRouter()

	// SanitizeRequestID MUST precede chimw.RequestID: chi adopts an inbound
	// X-Request-Id verbatim, and that value is trusted for log correlation and
	// propagated on outbound calls (internal/httpclient). The wrapper strips a
	// hostile/oversized inbound header so chi mints a fresh trusted ID, while
	// preserving a well-formed one for genuine cross-service correlation.
	publicRouter.Use(middleware.SanitizeRequestID)
	publicRouter.Use(chimw.RequestID)
	// RealIP resolves X-Forwarded-For / X-Real-IP onto r.RemoteAddr. When
	// registered it must run BEFORE RequestLogger so the logged "ip" field is
	// the client IP, not the upstream proxy.
	//
	// SECURITY: RealIP trusts the X-Forwarded-For / X-Real-IP headers, which are
	// client-spoofable. It is therefore OPT-IN via TRUST_PROXY_HEADERS (default
	// false) and only registered when the app sits behind a trusted reverse
	// proxy / load balancer that overwrites those headers on every request. With
	// the default false, the headers are ignored and r.RemoteAddr is the real
	// peer, safe for an app exposed directly to the internet.
	if cfg.Server.TrustProxyHeaders {
		publicRouter.Use(chimw.RealIP)
	}
	publicRouter.Use(middleware.RequestLogger(appLogger))
	publicRouter.Use(chimw.Recoverer)
	publicRouter.Use(middleware.SecurityHeaders)
	// CORS is registered BEFORE the metrics middleware, so it wraps it (chi runs
	// middleware in registration order, outer-first). The CORS middleware
	// short-circuits every preflight OPTIONS request with 204 and returns
	// WITHOUT calling next, so preflights never reach appMetrics.Middleware and
	// do NOT appear in http.server.request.count. This is intentional: CORS
	// preflights are browser-protocol overhead, not application requests, and
	// counting them would inflate the request metrics with traffic the handlers
	// never see.
	// (To meter preflights instead, register appMetrics.Middleware before CORS.)
	publicRouter.Use(middleware.CORS(middleware.CORSConfig{
		AllowedOrigins:   cfg.CORS.AllowedOrigins,
		AllowCredentials: cfg.CORS.AllowCredentials,
	}))
	publicRouter.Use(appMetrics.Middleware)

	// Application-level rate limiting is a FALLBACK, not the primary control.
	// The PRIMARY rate-limit control for this template is the network edge
	// (load balancer / API gateway / WAF). See README "Deployment
	// requirements". This middleware exists only for deployments that have no
	// edge limiter, and as the future home of per-USER limits once auth lands
	// (re-key by user ID then, for business-aware per-endpoint limits).
	//
	// Disabled by default: RATE_LIMIT_RPM=0 leaves rateLimiter nil and the
	// middleware is never applied. When enabled it keys by client IP and is
	// applied ONLY to the business-route group below (so the 429 still lands
	// after the metrics middleware and is counted in http.server.request.count).
	// NOT to /livez, /readyz, /health. Liveness/readiness probes (the container
	// HEALTHCHECK every 10s, kubelet probes sharing a source-IP bucket) must
	// never be 429'd into a restart loop; health endpoints rely on the readiness
	// cache and edge controls instead of the app limiter.
	//
	// IMPORTANT: httprate counters are IN-MEMORY PER INSTANCE, so the effective
	// global limit is RATE_LIMIT_RPM × replica count. A true global limit
	// belongs at the edge. And because it keys by IP, a deployment behind a
	// proxy WITHOUT TRUST_PROXY_HEADERS sees every request as the proxy's single
	// IP, dumping all traffic into one bucket and self-DoSing the API (warned
	// about at startup below).
	var rateLimiter func(http.Handler) http.Handler
	if cfg.Server.RateLimitRPM > 0 {
		if !cfg.Server.TrustProxyHeaders {
			appLogger.LogWarn(
				"RATE_LIMIT_RPM is enabled while TRUST_PROXY_HEADERS is false: "+
					"if this app is behind a proxy, all requests share the proxy's IP and fall into a single "+
					"rate-limit bucket, which self-DoSes the API. Enable TRUST_PROXY_HEADERS only behind a trusted proxy.",
				"rate_limit_rpm", cfg.Server.RateLimitRPM,
			)
		}
		rateLimiter = httprate.Limit(
			cfg.Server.RateLimitRPM,
			time.Minute,
			httprate.WithKeyByIP(),
			httprate.WithLimitHandler(func(w http.ResponseWriter, _ *http.Request) {
				shared.WriteJSONError(w, http.StatusTooManyRequests, shared.ErrTypeRateLimited,
					"rate limit exceeded; retry later")
			}),
		)
	}

	publicRouter.Use(chimw.Timeout(cfg.Server.RequestTimeout))
	// Limit request bodies to 1MB to prevent memory exhaustion.
	publicRouter.Use(chimw.RequestSize(1 * 1024 * 1024))

	// Router-level fallbacks on the PUBLIC router: chi's defaults answer
	// unmatched paths with plain-text "404 page not found" and bare 405s, which
	// bypass the single JSON-envelope rule. Override them so every public
	// response (including these) uses shared.WriteJSONError. These are
	// router-level (no feature label) and are NOT recorded on api.errors.
	// http.server.request.count already counts them via the metrics middleware.
	//
	// Known intentional gaps (out of scope: those middlewares own their bodies):
	// chimw.Timeout answers a timed-out request with a body-less 504, and
	// chimw.Recoverer answers a panic with a body-less 500. Replacing those
	// would mean reimplementing the middlewares.
	registerEnvelopeFallbacks(publicRouter)
	// The internal admin router (built below) deliberately keeps chi's plain-text
	// defaults: it is an operator-only surface (oncall), not a public API, so
	// envelope consistency there is unnecessary.

	// Liveness probe: minimal check that the process is responsive.
	// Does NOT check downstream dependencies, so a transient DB outage will
	// not cause the orchestrator to kill an otherwise-healthy pod. Use this
	// for Kubernetes livenessProbe. Kept on the public router so the
	// container HEALTHCHECK (which targets :PORT/livez) keeps working.
	// Single liveness handler shared by both listeners (defined once to avoid a
	// duplicated closure).
	livezHandler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"alive"}`))
	}
	publicRouter.Get("/livez", livezHandler)

	// Readiness probe: verifies the application is ready to serve traffic
	// by checking dependencies (currently the database). Returns 503 if the
	// app is up but unable to handle requests. Use this for Kubernetes
	// readinessProbe and load balancer health checks.
	//
	// Uses logger.FromContext so the LogError line emitted on a failed
	// readiness check carries request_id, method, path, and ip, the same
	// shape as any other in-request log. Falls back to appLogger if no
	// request-scoped logger is present (e.g. internal-router probe).
	// Cache the readiness result for a short TTL so a flood of probes on the
	// public listener cannot multiply DB pings (an unauthenticated
	// resource-consumption vector). The cached ping is computed on a context
	// detached from the requesting client's cancellation, so a client that
	// aborts mid-ping cannot cache a failure that the real probe would then see.
	readinessCache := db.NewHealthCache(database.HealthCheck, readinessCacheTTL)
	readyHandler := func(w http.ResponseWriter, r *http.Request) {
		if err := readinessCache.Check(r.Context()); err != nil {
			log := logger.FromContext(r.Context(), appLogger)
			log.LogError(errors.New("readiness check failed"), err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unhealthy"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"healthy"}`))
	}
	// Readiness is served on the public listener only when PUBLIC_READINESS is
	// true (the default). Set it false to keep readiness (a DB ping plus an
	// operational-state signal) on the internal admin port only, off the public
	// boundary. /livez (above) stays public regardless so the container
	// HEALTHCHECK keeps working.
	if cfg.Server.PublicReadiness {
		publicRouter.Get("/readyz", readyHandler)
		// /health is retained as an alias for /readyz for backwards
		// compatibility with deployments that already point health checks at it.
		publicRouter.Get("/health", readyHandler)
	}

	// Wire up log feature. The handler emits api_errors_total via appMetrics.
	// The service takes the pool-backed repository for single-row paths AND the
	// *db.DB itself as the transaction runner for multi-statement atomic paths
	// (e.g. POST /logs/batch). See userlog.NewLogService.
	logRepo := userlog.NewLogRepository(database.Pool())
	logService := userlog.NewLogService(logRepo, database)
	logHandler := userlog.NewLogHandler(logService, appLogger, appMetrics)

	// Business routes live in a Group so the app-level rate limiter (when
	// enabled) applies to them (and to any future business routes added here)
	// but NOT to the health endpoints registered above, which sit outside this
	// group and are therefore never rate-limited.
	publicRouter.Group(func(r chi.Router) {
		if rateLimiter != nil {
			r.Use(rateLimiter)
		}

		// API routes.
		r.Route("/api/v1", func(r chi.Router) {
			// Two-layer auth model (see .claude/rules/decisions.md #13/#17). Both
			// layers are opt-in; a disabled layer is nil and simply not registered,
			// so local dev with no IdP / no OPA still works.
			//
			// Layer 1 (OIDC, authn): validates the Bearer access token against the
			// IdP's discovered JWKS and stores the caller's internal UUID via
			// shared.WithUserID. Handlers read it back via shared.UserIDFromContext.
			// The key lives in internal/shared so this infrastructure middleware
			// never imports a feature package.
			//
			// Layer 2 (OPA, authz): FUNCTION-LEVEL authorisation over OPA's REST Data
			// API, fail closed. It does NOT replace the repository's WHERE user_id =
			// $N row-ownership scoping — the two layers stay SEPARATE (OPA: "may this
			// role call this endpoint"; SQL: "may this user see this row").
			//
			// The ORDER (authn before authz) and the chi REGISTRATION KIND both
			// matter: authz MUST be an inline-Group middleware so it runs AFTER chi
			// has resolved the leaf route pattern, otherwise OPA receives the mount
			// pattern "/api/v1/*" and denies everything. middleware.AuthorizedRoutes
			// encapsulates that wiring (and is exercised by the middleware tests), so
			// this call is the single source of truth for it.
			var authn, authz func(http.Handler) http.Handler
			if oidcAuth != nil {
				authn = oidcAuth.Handler
			}
			if opaAuthz != nil {
				authz = opaAuthz.Handler
			}
			// Each feature registers its own routes inside the authorised group.
			// Adding a feature is one line here. See userlog.Handler.Routes.
			middleware.AuthorizedRoutes(r, authn, authz, logHandler.Routes)
		})
	})

	// Build the internal admin router (:METRICS_PORT). This is for operator-only
	// endpoints (the mirrored health probes). It must be reachable from oncall
	// but not from the public internet, so restrict via NetworkPolicy / firewall
	// / private network at the infrastructure layer. No CORS, no request body
	// limit, no auth.
	//
	// It no longer serves /metrics: metrics are pushed to the Collector over
	// OTLP, not scraped from the app. The metrics middleware still wraps this
	// router so admin-port requests are counted like any other.
	internalRouter := chi.NewRouter()
	internalRouter.Use(chimw.Recoverer)
	internalRouter.Use(appMetrics.Middleware)
	// Mirror all three health endpoints on the internal port so k8s probes (and
	// operators) can target the internal listener without exposing the public
	// API, and so readiness stays reachable even when PUBLIC_READINESS is false.
	internalRouter.Get("/livez", livezHandler)
	internalRouter.Get("/readyz", readyHandler)
	internalRouter.Get("/health", readyHandler)

	// Wrap the public router in otelhttp so every request gets a server span,
	// which becomes the active span in the request context. This is the single
	// clean wrap point: it sits OUTSIDE chi, so the chi middleware order is
	// untouched, and because the span is created before chi runs, RequestLogger
	// can read trace_id/span_id off the context (see internal/middleware) and the
	// metrics middleware can attach exemplars. When telemetry is disabled the
	// global tracer is a no-op, so the span is a cheap no-op.
	//
	// A no-op MeterProvider is passed so otelhttp does NOT also emit its own
	// http.server.* metrics, whose names would collide with the equivalents our
	// own metrics middleware records. otelhttp here is for spans only.
	publicHandler := otelhttp.NewHandler(publicRouter, "http.server.request",
		otelhttp.WithMeterProvider(metricnoop.NewMeterProvider()))

	publicServer := &http.Server{
		Addr:              ":" + cfg.Server.Port,
		Handler:           publicHandler,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
	}

	internalServer := &http.Server{
		Addr:              ":" + cfg.Server.MetricsPort,
		Handler:           internalRouter,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
	}

	// Push server start errors onto a channel rather than os.Exit'ing inside
	// goroutines. Exiting there would skip the graceful cleanup below
	// and leak DB connections.
	serverErrCh := make(chan error, 2)
	go func() {
		appLogger.LogInfo("public api server listening", "port", cfg.Server.Port)
		if err := publicServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrCh <- fmt.Errorf("public server: %w", err)
		}
	}()
	go func() {
		appLogger.LogInfo("internal admin server listening", "port", cfg.Server.MetricsPort)
		if err := internalServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrCh <- fmt.Errorf("internal server: %w", err)
		}
	}()

	// Graceful shutdown on SIGINT, SIGTERM, or server error.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	exitCode := 0
	select {
	case <-quit:
		appLogger.LogInfo("shutting down servers")
	case err := <-serverErrCh:
		appLogger.LogError(errors.New("server error"), err)
		exitCode = 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := publicServer.Shutdown(ctx); err != nil {
		appLogger.LogError(errors.New("public server shutdown error"), err)
	}
	if err := internalServer.Shutdown(ctx); err != nil {
		appLogger.LogError(errors.New("internal server shutdown error"), err)
	}

	// Flush and shut down telemetry AFTER the servers stop (so the final spans
	// and metric interval are captured) but BEFORE the DB closes (so any
	// shutdown-path spans still export). A no-op when telemetry is disabled.
	if err := otelShutdown(ctx); err != nil {
		appLogger.LogError(errors.New("telemetry shutdown error"), err)
	}

	appLogger.LogInfo("closing database connection")
	database.Close()

	appLogger.LogInfo("server stopped")
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
