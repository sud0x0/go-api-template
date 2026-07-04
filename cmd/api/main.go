// Command api is the go-api-template HTTP server entrypoint. It wires
// configuration, database, metrics, logging, and middleware, then serves the
// public API on :PORT and the internal admin listener (metrics and health) on
// :METRICS_PORT. See the README "Quick start" to run it.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/sud0x0/go-api-template/internal/config"
	"github.com/sud0x0/go-api-template/internal/db"
	"github.com/sud0x0/go-api-template/internal/metrics"
	"github.com/sud0x0/go-api-template/internal/middleware"
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
// PUBLIC router only — the internal admin router keeps chi defaults.
//
// These are router-level errors (no feature label) and are deliberately NOT
// recorded on api_errors_total; http_requests_total already counts them via the
// metrics middleware.
//
// chi sets the Allow header on a 405 only through its DEFAULT handler; a custom
// MethodNotAllowed handler is not given the allowed-methods list (it is
// unexported on the route context). WriteJSONError does not touch the Allow
// header, so any value already set upstream is preserved — in practice chi sets
// none for a custom handler, so 405s carry the envelope but no Allow header.
func registerEnvelopeFallbacks(r chi.Router) {
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		shared.WriteJSONError(w, http.StatusNotFound, shared.ErrTypeNotFound, "resource not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, shared.ErrTypeMethodNotAllowed, "method not allowed")
	})
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
	// commit — see internal/shared/logger.NewLogger.
	appLogger := logger.NewLogger(cfg.Log.Level, "go-api")
	// Install the app logger as slog's process-wide default so any code logging
	// via the slog package default routes through our handler. Done here (not in
	// the constructor) so constructing a logger never mutates global state.
	logger.SetDefaultSlog(appLogger)
	appLogger.LogInfo("starting application")

	// Initialise Prometheus metrics FIRST so the pgx pool can be created
	// with the query tracer wired in.
	appMetrics := metrics.New()

	// Each feature exports the tables it needs; main aggregates them so the
	// startup schema check never couples internal/db to a feature package. Add
	// a new feature's RequiredTables to this Concat call.
	requiredTables := slices.Concat(
		userlog.RequiredTables,
	)

	// Initialise database with the query tracer. Every query on this pool
	// records into db_query_duration_seconds and db_query_errors_total
	// (with pgx.ErrNoRows excluded from the error counter).
	database, err := db.New(&cfg.Database, appLogger, appMetrics.QueryTracer(), requiredTables)
	if err != nil {
		appLogger.LogError(err, nil)
		os.Exit(1)
	}

	// Register the pool stats collector. It reads pool.Stat() at every
	// scrape — no goroutine or background polling.
	prometheus.MustRegister(metrics.NewPoolCollector(database.Pool()))

	// Build the public API router (:PORT).
	publicRouter := chi.NewRouter()

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
	// peer — safe for an app exposed directly to the internet.
	if cfg.Server.TrustProxyHeaders {
		publicRouter.Use(chimw.RealIP)
	}
	publicRouter.Use(middleware.RequestLogger(appLogger))
	publicRouter.Use(chimw.Recoverer)
	publicRouter.Use(middleware.SecurityHeaders)
	// CORS is registered BEFORE the metrics middleware, so it wraps it (chi runs
	// middleware in registration order, outer-first). The CORS middleware
	// short-circuits every preflight OPTIONS request with 204 and returns
	// WITHOUT calling next — so preflights never reach appMetrics.Middleware and
	// do NOT appear in http_requests_total. This is intentional: CORS preflights
	// are browser-protocol overhead, not application requests, and counting them
	// would inflate the request metrics with traffic the handlers never see.
	// (To meter preflights instead, register appMetrics.Middleware before CORS.)
	publicRouter.Use(middleware.CORS(middleware.CORSConfig{
		AllowedOrigins:   cfg.CORS.AllowedOrigins,
		AllowCredentials: cfg.CORS.AllowCredentials,
	}))
	publicRouter.Use(appMetrics.Middleware)

	// Application-level rate limiting is a FALLBACK, not the primary control.
	// The PRIMARY rate-limit control for this template is the network edge
	// (load balancer / API gateway / WAF) — see README "Deployment
	// requirements". This middleware exists only for deployments that have no
	// edge limiter, and as the future home of per-USER limits once auth lands
	// (re-key by user ID then, for business-aware per-endpoint limits).
	//
	// Disabled by default: RATE_LIMIT_RPM=0 leaves rateLimiter nil and the
	// middleware is never applied. When enabled it keys by client IP and is
	// applied ONLY to the business-route group below (so the 429 still lands
	// after the metrics middleware and is counted in http_requests_total) — NOT
	// to /livez, /readyz, /health. Liveness/readiness probes (the container
	// HEALTHCHECK every 10s, kubelet probes sharing a source-IP bucket) must
	// never be 429'd into a restart loop; health endpoints rely on the readiness
	// cache and edge controls instead of the app limiter.
	//
	// IMPORTANT: httprate counters are IN-MEMORY PER INSTANCE, so the effective
	// global limit is RATE_LIMIT_RPM × replica count — a true global limit
	// belongs at the edge. And because it keys by IP, a deployment behind a
	// proxy WITHOUT TRUST_PROXY_HEADERS sees every request as the proxy's single
	// IP, dumping all traffic into one bucket and self-DoSing the API (warned
	// about at startup below).
	var rateLimiter func(http.Handler) http.Handler
	if cfg.Server.RateLimitRPM > 0 {
		if !cfg.Server.TrustProxyHeaders {
			appLogger.LogWarn(
				"RATE_LIMIT_RPM is enabled while TRUST_PROXY_HEADERS is false — "+
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
	// response — including these — uses shared.WriteJSONError. These are
	// router-level (no feature label) and are NOT recorded on api_errors_total;
	// http_requests_total already counts them via the metrics middleware.
	//
	// Known intentional gaps (out of scope — those middlewares own their bodies):
	// chimw.Timeout answers a timed-out request with a body-less 504, and
	// chimw.Recoverer answers a panic with a body-less 500. Replacing those
	// would mean reimplementing the middlewares.
	registerEnvelopeFallbacks(publicRouter)
	// The internal admin router (built below) deliberately keeps chi's plain-text
	// defaults: it is an operator-only surface (Prometheus/oncall), not a public
	// API, so envelope consistency there is unnecessary.

	// Liveness probe — minimal check that the process is responsive.
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

	// Readiness probe — verifies the application is ready to serve traffic
	// by checking dependencies (currently the database). Returns 503 if the
	// app is up but unable to handle requests. Use this for Kubernetes
	// readinessProbe and load balancer health checks.
	//
	// Uses logger.FromContext so the LogError line emitted on a failed
	// readiness check carries request_id, method, path, and ip — same
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
	// true (the default). Set it false to keep readiness — a DB ping plus an
	// operational-state signal — on the internal admin port only, off the public
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
	// (e.g. POST /logs/batch) — see userlog.NewLogService.
	logRepo := userlog.NewLogRepository(database.Pool())
	logService := userlog.NewLogService(logRepo, database)
	logHandler := userlog.NewLogHandler(logService, appLogger, appMetrics)

	// Business routes live in a Group so the app-level rate limiter (when
	// enabled) applies to them — and to any future business routes added here —
	// but NOT to the health endpoints registered above, which sit outside this
	// group and are therefore never rate-limited.
	publicRouter.Group(func(r chi.Router) {
		if rateLimiter != nil {
			r.Use(rateLimiter)
		}

		// API routes.
		r.Route("/api/v1", func(r chi.Router) {
			// TODO(auth): wire auth middleware here — see .claude/rules/decisions.md
			// #13 (auth is intentionally contract-only). Any middleware that validates
			// a token and stores the user ID on the request context with
			// shared.WithUserID(ctx, userID) will work automatically — handlers read
			// it back via shared.UserIDFromContext. The key lives in internal/shared
			// so infrastructure middleware never imports a feature package. All
			// repository queries are scoped by user_id.
			//
			// CONTRACT: the user ID MUST be a UUID (the user_id column is UUID NOT
			// NULL; UserIDFromContext rejects non-UUIDs as 401). An external IdP
			// whose `sub` is not a UUID (e.g. "auth0|12345", an email) needs a
			// mapping table from sub → internal UUID; resolve it in the middleware
			// before calling shared.WithUserID.
			// Example:
			//   r.Use(authMiddleware.Handler)

			// Each feature registers its own routes — adding a feature is one line
			// here, not five. See userlog.Handler.Routes.
			logHandler.Routes(r)
		})
	})

	// Build the internal admin router (:METRICS_PORT). This is for /metrics
	// and any other operator-only endpoints. It must be reachable from
	// Prometheus scrapers and oncall, but not from the public internet —
	// restrict via NetworkPolicy / firewall / private network at the
	// infrastructure layer. No CORS, no request body limit, no auth.
	//
	// The metrics middleware is applied here too so scrapes themselves are
	// recorded (no recursion — the counter increment lands on the NEXT
	// scrape's snapshot).
	internalRouter := chi.NewRouter()
	internalRouter.Use(chimw.Recoverer)
	internalRouter.Use(appMetrics.Middleware)
	internalRouter.Handle("/metrics", metrics.Handler())
	// Mirror all three health endpoints on the internal port so k8s probes (and
	// operators) can target the internal listener without exposing the public
	// API — and so readiness stays reachable even when PUBLIC_READINESS is false.
	internalRouter.Get("/livez", livezHandler)
	internalRouter.Get("/readyz", readyHandler)
	internalRouter.Get("/health", readyHandler)

	publicServer := &http.Server{
		Addr:              ":" + cfg.Server.Port,
		Handler:           publicRouter,
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
	// goroutines — exiting there would skip the graceful cleanup below
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

	appLogger.LogInfo("closing database connection")
	database.Close()

	appLogger.LogInfo("server stopped")
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
