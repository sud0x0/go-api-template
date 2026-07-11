// Package config loads and validates all environment configuration into typed
// structs at startup. It is the single place os.Getenv is called; malformed or
// invalid values fail fast with the offending variable name rather than
// silently defaulting.
package config

import (
	"fmt"
	"math"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultPort is the default public HTTP port. It is defined once here and
// reused by both config.Load (the listener) and cmd/api/cli.go (the
// --healthcheck probe) so the two can never drift.
const DefaultPort = "8080"

// ResolvePort returns the public port the server listens on: $PORT if set,
// otherwise DefaultPort. The --healthcheck CLI uses this to target the same
// port the server binds, so the container HEALTHCHECK no longer needs a --port
// flag kept in sync with PORT (the flag remains as an override).
func ResolvePort() string {
	return getEnv("PORT", DefaultPort)
}

// Config holds all application configuration loaded from environment variables.
// Load this once at startup and pass it explicitly to components that need it.
type Config struct {
	Server        ServerConfig
	Database      DatabaseConfig
	Log           LogConfig
	CORS          CORSConfig
	Observability ObservabilityConfig
	Auth          AuthConfig
}

// ServerConfig holds HTTP server configuration.
type ServerConfig struct {
	Port              string
	MetricsPort       string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	RequestTimeout    time.Duration
	// TrustProxyHeaders gates chimw.RealIP. When false (the default), the
	// spoofable X-Forwarded-For / X-Real-IP headers are ignored and r.RemoteAddr
	// is the real peer. Set true ONLY when a trusted reverse proxy / load
	// balancer overwrites these headers on every request.
	TrustProxyHeaders bool
	// RateLimitRPM is the app-level requests-per-minute-per-IP cap. 0 (the
	// default) disables the limiter entirely: the middleware is not even
	// registered. The PRIMARY rate-limit control is the edge (LB / gateway /
	// WAF); this is only a fallback for edge-less deployments. The counters are
	// in-memory per instance, so the effective global limit is RateLimitRPM ×
	// replica count.
	RateLimitRPM int
	// PublicReadiness controls whether /readyz and /health are served on the
	// PUBLIC listener. Default true for backwards compatibility. Set false to
	// keep readiness (a DB ping and an operational-state signal) on the internal
	// admin port only, off the public boundary. /livez stays public regardless.
	PublicReadiness bool
}

// DatabaseConfig holds database connection configuration.
type DatabaseConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	Name     string
	// SSLMode is the libpq sslmode for the connection (DB_SSLMODE, default
	// "require"). SECURITY: "require" ENCRYPTS the link but does NOT authenticate
	// the server: it performs no CA or hostname verification, so it does not stop
	// a man-in-the-middle presenting its own certificate. For any deployment where
	// the DB link crosses an untrusted network, use "verify-full" with a CA cert
	// (DB_SSLMODE=verify-full plus the CA in the connection environment). The
	// default stays "require" so local/compose dev works without cert material;
	// harden it in production. See the README "Database" section.
	SSLMode         string
	MaxOpenConns    int
	MinConns        int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// LogConfig holds logging configuration.
type LogConfig struct {
	Level string
}

// CORSConfig holds CORS middleware configuration.
type CORSConfig struct {
	AllowedOrigins   []string
	AllowCredentials bool
}

// ObservabilityConfig holds OpenTelemetry (OTel) export settings. Traces and
// metrics are pushed over OTLP to a local Collector, which batches them and
// re-exposes Prometheus for scraping (see the README "Observability" section).
// Logs are NOT covered here: they stay on stdlib slog JSON to stdout.
type ObservabilityConfig struct {
	// OTELEnabled gates all OTel wiring. When false the app installs no-op
	// tracer/meter providers and boots with telemetry disabled (logged once at
	// startup), so a template with no Collector still runs. When OTEL_ENABLED is
	// unset it defaults to "an endpoint is configured", so setting the endpoint
	// is enough to turn telemetry on with no second flag to remember.
	OTELEnabled bool
	// OTELExporterEndpoint is the OTLP Collector endpoint from
	// OTEL_EXPORTER_OTLP_ENDPOINT (e.g. http://otel-collector:4318 for the HTTP
	// exporter this template uses). Empty disables telemetry. No localhost
	// default is baked in here: an unset endpoint means "disabled", never
	// "silently push somewhere wrong".
	OTELExporterEndpoint string
	// OTELServiceName is the service.name resource attribute from
	// OTEL_SERVICE_NAME. Defaults to "go-api" to match the logger's service
	// attribute, so a log line and its trace share one service identity.
	OTELServiceName string
}

// AuthConfig holds the two-layer auth model's settings. It is a dedicated
// struct (parallel to ObservabilityConfig) rather than fields tacked onto
// ServerConfig because auth spans two distinct subsystems, authentication
// (OIDC token validation, in-process) and authorisation (OPA sidecar over REST),
// and keeping them together localises both the main() wiring and the
// validate() fail-fast rules to one place.
//
// Both layers are OFF by default so the template boots with no IdP and no OPA
// for local dev. Each derives its Enabled flag from "is its endpoint set",
// mirroring the OTEL_ENABLED pattern (one variable turns a layer on).
type AuthConfig struct {
	OIDC OIDCConfig
	OPA  OPAConfig
}

// OIDCConfig holds the resource-server authentication settings. The template is
// an OAuth2 RESOURCE SERVER: it validates a Bearer access token against the
// IdP's discovered JWKS. It is NOT an authorization server and NOT a browser
// OIDC client: the login redirect, code exchange, PKCE, and nonce belong to the
// adopter's IdP and frontend (see .claude/rules/decisions.md).
type OIDCConfig struct {
	// Enabled gates the authentication middleware. When false the middleware is
	// not registered and /api/v1 runs unauthenticated (logged once at startup),
	// so the template runs with no IdP for local dev. When OIDC_ENABLED is unset
	// it defaults to "an issuer is configured", so setting OIDC_ISSUER_URL is
	// enough to turn auth on with no second flag to remember.
	Enabled bool
	// IssuerURL is the OIDC issuer / discovery base from OIDC_ISSUER_URL (e.g.
	// https://accounts.example.com). go-oidc appends /.well-known/openid-
	// configuration to it and validates that the discovered issuer matches this
	// value EXACTLY (ASVS V10.5.3). Empty disables auth.
	IssuerURL string
	// Audience is the expected token audience from OIDC_AUDIENCE. Set it to THIS
	// API's own resource/audience identifier — the value your IdP stamps into the
	// `aud` of ACCESS tokens issued for this API (e.g. an API identifier such as
	// "https://api.example.com" or a resource/scope audience). It is passed to the
	// verifier as ClientID so the `aud` claim is validated (ASVS V9.2.3 / V10.3.1).
	//
	// SECURITY — do NOT set this to a browser/SPA client_id. An OIDC ID token's
	// `aud` IS the client_id, so if OIDC_AUDIENCE equals a client_id the audience
	// check stops distinguishing ID tokens from access tokens and an ID token
	// would pass audience validation (token-type confusion). Using the API's own
	// distinct audience makes an ID token (aud = client_id) fail this check. The
	// middleware additionally rejects ID-token-only claims as defence in depth.
	//
	// Required when OIDC is enabled: an empty audience would force go-oidc's
	// SkipClientIDCheck, which we never do.
	Audience string
}

// OPAConfig holds the Open Policy Agent authorisation settings. After
// authentication, the authz middleware asks an OPA sidecar (over its REST Data
// API) whether {principal, action, resource} is allowed and enforces the result.
// This is FUNCTION-LEVEL authorisation only: the repository's WHERE user_id =
// $N SQL scoping stays as the row-ownership layer (defence in depth).
type OPAConfig struct {
	// Enabled gates the authorisation middleware. When false it is not registered
	// (logged once at startup). When OPA_ENABLED is unset it defaults to "a URL is
	// configured", mirroring OIDC/OTEL.
	Enabled bool
	// URL is the OPA sidecar base URL from OPA_URL (e.g. http://opa:8181). Empty
	// disables authz.
	URL string
	// DecisionPath is the OPA REST Data API path to the boolean decision from
	// OPA_DECISION_PATH (e.g. v1/data/api/authz/allow). It must match the Rego
	// package + rule the policy defines. Leading/trailing slashes are trimmed when
	// the URL is built.
	DecisionPath string
	// Timeout bounds the OPA HTTP call. Kept short (default 100ms) because it sits
	// on every request's hot path: a slow or unreachable OPA must fail closed
	// quickly, not hang the request. Sourced from OPA_TIMEOUT_MS.
	Timeout time.Duration
}

// LoadDatabase reads the DB_* environment variables and returns a validated
// DatabaseConfig. Use this from cmd/migrate and anywhere the API's full
// configuration is unnecessary. Load() calls this so the two paths never
// drift.
func LoadDatabase() (*DatabaseConfig, error) {
	maxOpen, err := getEnvInt("DB_MAX_OPEN_CONNS", 100)
	if err != nil {
		return nil, err
	}
	minConns, err := getEnvInt("DB_MIN_CONNS", 10)
	if err != nil {
		return nil, err
	}
	connMaxLifetime, err := getEnvInt("DB_CONN_MAX_LIFETIME_MINS", 5)
	if err != nil {
		return nil, err
	}
	connMaxIdleTime, err := getEnvInt("DB_CONN_MAX_IDLE_TIME_MINS", 10)
	if err != nil {
		return nil, err
	}

	cfg := &DatabaseConfig{
		Host:            getEnv("DB_HOST", ""),
		Port:            getEnv("DB_PORT", ""),
		User:            getEnv("DB_USER", ""),
		Password:        getEnv("DB_PASSWORD", ""),
		Name:            getEnv("DB_NAME", ""),
		SSLMode:         getEnv("DB_SSLMODE", "require"),
		MaxOpenConns:    maxOpen,
		MinConns:        minConns,
		ConnMaxLifetime: time.Duration(connMaxLifetime) * time.Minute,
		ConnMaxIdleTime: time.Duration(connMaxIdleTime) * time.Minute,
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validate verifies the DatabaseConfig has the required fields populated and
// the connection-pool sizes are within range. Bounding the pool sizes here is
// the operator-friendly fail-fast layer: a value like DB_MAX_OPEN_CONNS=
// 5000000000 is rejected at startup with a clear message rather than silently
// wrapping in the int32() conversion db.New does for pgxpool. These are the
// SEMANTIC rules (>= 1, the MinConns <= MaxOpenConns relation); db.toInt32
// separately guards the CONVERSION range.
func (c *DatabaseConfig) validate() error {
	if c.Host == "" || c.Port == "" ||
		c.User == "" || c.Password == "" || c.Name == "" {
		return fmt.Errorf("missing required database environment variables (DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, DB_NAME)")
	}
	if c.MaxOpenConns < 1 || c.MaxOpenConns > math.MaxInt32 {
		return fmt.Errorf("DB_MAX_OPEN_CONNS must be in [1, %d], got %d", math.MaxInt32, c.MaxOpenConns)
	}
	if c.MinConns < 0 || c.MinConns > c.MaxOpenConns {
		return fmt.Errorf("DB_MIN_CONNS must be in [0, DB_MAX_OPEN_CONNS (%d)], got %d", c.MaxOpenConns, c.MinConns)
	}
	return nil
}

// MigratorConfig holds the migrator binary's configuration. It composes
// DatabaseConfig with migrator-specific timeouts that constrain how long
// a single migration may block production traffic, plus the destructive-
// command gate value. Reading these through one config struct keeps cmd/
// migrate free of scattered os.Getenv calls.
type MigratorConfig struct {
	Database         DatabaseConfig
	LockTimeout      string // Postgres duration string (e.g. "5s")
	StatementTimeout string // Postgres duration string (e.g. "10min")
	AllowDestructive string // MIGRATOR_ALLOW_DESTRUCTIVE: "true" gates destructive subcommands (paired with --confirm-destructive)
}

// LoadMigrator reads the migrator binary's env vars. It reuses LoadDatabase
// for DB connection settings and adds MIGRATOR_LOCK_TIMEOUT and
// MIGRATOR_STATEMENT_TIMEOUT. Both timeouts are validated as Postgres
// duration strings before being passed to pgx as runtime parameters.
func LoadMigrator() (*MigratorConfig, error) {
	dbCfg, err := LoadDatabase()
	if err != nil {
		return nil, err
	}
	cfg := &MigratorConfig{
		Database:         *dbCfg,
		LockTimeout:      getEnv("MIGRATOR_LOCK_TIMEOUT", "5s"),
		StatementTimeout: getEnv("MIGRATOR_STATEMENT_TIMEOUT", "10min"),
		AllowDestructive: getEnv("MIGRATOR_ALLOW_DESTRUCTIVE", ""),
	}
	if !isPostgresDuration(cfg.LockTimeout) {
		return nil, fmt.Errorf("MIGRATOR_LOCK_TIMEOUT %q must be a positive Postgres duration with an explicit unit (e.g. 5s, 100ms, 1min, 2h); a bare integer or 0 is rejected (0 disables the timeout)", cfg.LockTimeout)
	}
	if !isPostgresDuration(cfg.StatementTimeout) {
		return nil, fmt.Errorf("MIGRATOR_STATEMENT_TIMEOUT %q must be a positive Postgres duration with an explicit unit (e.g. 5s, 100ms, 1min, 2h); a bare integer or 0 is rejected (0 disables the timeout)", cfg.StatementTimeout)
	}
	return cfg, nil
}

// postgresDurationRE matches a Postgres duration for the migrator: a positive
// integer followed by a MANDATORY unit. Two things it deliberately rejects:
//   - A bare integer. Postgres would interpret a unitless lock_timeout /
//     statement_timeout as MILLISECONDS, so "5" silently means 5ms — an easy way
//     to set an absurdly short timeout by accident. Requiring an explicit unit
//     removes the ambiguity.
//   - A zero value ("0", "0s", "00min"). In Postgres, lock_timeout /
//     statement_timeout of 0 means "DISABLED / wait forever", which silently
//     defeats the fail-fast the migrator timeouts exist to provide. The numeric
//     part is checked to be > 0 in isPostgresDuration.
var postgresDurationRE = regexp.MustCompile(`^\s*([0-9]+)\s*(us|ms|s|min|h|d)\s*$`)

func isPostgresDuration(s string) bool {
	m := postgresDurationRE.FindStringSubmatch(s)
	if m == nil {
		return false // not a positive integer + explicit unit
	}
	n, err := strconv.Atoi(m[1])
	return err == nil && n > 0 // reject zero: it means "disabled" in Postgres
}

// Load reads all environment variables and returns a validated Config.
// Returns an error if required variables are missing or malformed.
func Load() (*Config, error) {
	dbCfg, err := LoadDatabase()
	if err != nil {
		return nil, err
	}

	readHeaderSecs, err := getEnvInt("SERVER_READ_HEADER_TIMEOUT_SECS", 5)
	if err != nil {
		return nil, err
	}
	readSecs, err := getEnvInt("SERVER_READ_TIMEOUT_SECS", 10)
	if err != nil {
		return nil, err
	}
	// WriteTimeout default deliberately exceeds RequestTimeout (60s) so the
	// handler timeout fires before the write window closes. A WriteTimeout
	// shorter than RequestTimeout would silently truncate slow responses.
	writeSecs, err := getEnvInt("SERVER_WRITE_TIMEOUT_SECS", 65)
	if err != nil {
		return nil, err
	}
	idleSecs, err := getEnvInt("SERVER_IDLE_TIMEOUT_SECS", 120)
	if err != nil {
		return nil, err
	}
	requestSecs, err := getEnvInt("SERVER_REQUEST_TIMEOUT_SECS", 60)
	if err != nil {
		return nil, err
	}

	allowCreds, err := getEnvBool("CORS_ALLOW_CREDENTIALS", false)
	if err != nil {
		return nil, err
	}

	trustProxy, err := getEnvBool("TRUST_PROXY_HEADERS", false)
	if err != nil {
		return nil, err
	}

	rateLimitRPM, err := getEnvInt("RATE_LIMIT_RPM", 0)
	if err != nil {
		return nil, err
	}
	if rateLimitRPM < 0 {
		return nil, fmt.Errorf("RATE_LIMIT_RPM must be >= 0 (0 disables the app-level limiter), got %d", rateLimitRPM)
	}

	publicReadiness, err := getEnvBool("PUBLIC_READINESS", true)
	if err != nil {
		return nil, err
	}

	// OTel: an unset OTEL_EXPORTER_OTLP_ENDPOINT means telemetry is disabled
	// rather than pointed at a guessed localhost. OTEL_ENABLED, when unset,
	// defaults to "an endpoint is configured" so a single variable turns the
	// pipeline on. Set it explicitly to false to force-disable even with an
	// endpoint present, or true to fail fast (in validate) if the endpoint is
	// missing.
	otelEndpoint := getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	otelEnabled, err := getEnvBool("OTEL_ENABLED", otelEndpoint != "")
	if err != nil {
		return nil, err
	}

	// Auth (OIDC + OPA). Both layers default to "enabled if their endpoint is
	// set" so a single variable turns each on, matching the OTEL_ENABLED pattern.
	// Left unset (no issuer, no OPA URL) the app boots with auth off for local
	// dev.
	oidcIssuer := getEnv("OIDC_ISSUER_URL", "")
	oidcEnabled, err := getEnvBool("OIDC_ENABLED", oidcIssuer != "")
	if err != nil {
		return nil, err
	}
	opaURL := getEnv("OPA_URL", "")
	opaEnabled, err := getEnvBool("OPA_ENABLED", opaURL != "")
	if err != nil {
		return nil, err
	}
	opaTimeoutMS, err := getEnvInt("OPA_TIMEOUT_MS", 100)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Server: ServerConfig{
			Port:              getEnv("PORT", DefaultPort),
			MetricsPort:       getEnv("METRICS_PORT", "9090"),
			ReadHeaderTimeout: time.Duration(readHeaderSecs) * time.Second,
			ReadTimeout:       time.Duration(readSecs) * time.Second,
			WriteTimeout:      time.Duration(writeSecs) * time.Second,
			IdleTimeout:       time.Duration(idleSecs) * time.Second,
			RequestTimeout:    time.Duration(requestSecs) * time.Second,
			TrustProxyHeaders: trustProxy,
			RateLimitRPM:      rateLimitRPM,
			PublicReadiness:   publicReadiness,
		},
		Database: *dbCfg,
		Log: LogConfig{
			// Default to production (info level), a fail-safe default so an
			// unset LOG_LEVEL never accidentally ships debug-level logs (which
			// can be verbose and may surface internals). Dev sets it explicitly.
			Level: strings.ToLower(getEnv("LOG_LEVEL", "production")),
		},
		CORS: CORSConfig{
			AllowedOrigins:   parseOrigins(getEnv("CORS_ALLOWED_ORIGINS", "")),
			AllowCredentials: allowCreds,
		},
		Observability: ObservabilityConfig{
			OTELEnabled:          otelEnabled,
			OTELExporterEndpoint: otelEndpoint,
			OTELServiceName:      getEnv("OTEL_SERVICE_NAME", "go-api"),
		},
		Auth: AuthConfig{
			OIDC: OIDCConfig{
				Enabled:   oidcEnabled,
				IssuerURL: oidcIssuer,
				Audience:  getEnv("OIDC_AUDIENCE", ""),
			},
			OPA: OPAConfig{
				Enabled:      opaEnabled,
				URL:          opaURL,
				DecisionPath: getEnv("OPA_DECISION_PATH", "v1/data/api/authz/allow"),
				Timeout:      time.Duration(opaTimeoutMS) * time.Millisecond,
			},
		},
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// validate checks the non-database configuration values. LoadDatabase
// already validated the Database section before Load embedded it.
func (c *Config) validate() error {
	// Positive-duration guard. A zero or negative timeout silently DISABLES the
	// protection it configures: Go's http.Server treats a zero Read/Write/Idle
	// timeout as "no timeout" (a slow-loris DoS surface), and a non-positive pool
	// lifetime/idle removes the bound that recycles stale connections. Fail fast
	// naming the offending variable rather than boot with the guard off (repo
	// convention: no silent fallback). This mirrors the OPA-timeout guard below;
	// RATE_LIMIT_RPM keeps its own >= 0 check because 0 there legitimately means
	// "limiter disabled", whereas a 0 timeout is never a valid configuration.
	for _, d := range []struct {
		name  string
		value time.Duration
	}{
		{"SERVER_READ_HEADER_TIMEOUT_SECS", c.Server.ReadHeaderTimeout},
		{"SERVER_READ_TIMEOUT_SECS", c.Server.ReadTimeout},
		{"SERVER_WRITE_TIMEOUT_SECS", c.Server.WriteTimeout},
		{"SERVER_IDLE_TIMEOUT_SECS", c.Server.IdleTimeout},
		{"SERVER_REQUEST_TIMEOUT_SECS", c.Server.RequestTimeout},
		{"DB_CONN_MAX_LIFETIME_MINS", c.Database.ConnMaxLifetime},
		{"DB_CONN_MAX_IDLE_TIME_MINS", c.Database.ConnMaxIdleTime},
	} {
		if d.value <= 0 {
			return fmt.Errorf("%s must be a positive duration, got %v", d.name, d.value)
		}
	}

	// A WriteTimeout shorter than RequestTimeout causes slow responses to be
	// truncated mid-write before the handler timeout fires, usually surfacing
	// as confusing "connection reset" reports. Fail at startup rather than ship.
	if c.Server.WriteTimeout <= c.Server.RequestTimeout {
		return fmt.Errorf(
			"SERVER_WRITE_TIMEOUT_SECS (%v) must be greater than SERVER_REQUEST_TIMEOUT_SECS (%v) "+
				"so the handler timeout fires before the write window closes",
			c.Server.WriteTimeout, c.Server.RequestTimeout,
		)
	}

	// The CORS middleware matches origins exactly (slices.Contains), so a
	// wildcard entry is silently inert: it would never match and contradicts
	// the documented "never `*` in production" rule while giving a false sense
	// that all origins are allowed. Fail loudly instead.
	for _, origin := range c.CORS.AllowedOrigins {
		if strings.Contains(origin, "*") {
			return fmt.Errorf(
				"CORS_ALLOWED_ORIGINS must not contain a wildcard (%q): the middleware matches "+
					"origins exactly, so a `*` entry is silently inert. List explicit origins, or "+
					"leave it empty for a server-to-server API",
				origin,
			)
		}
	}

	// Credentialed CORS with no allowed origins can never succeed (the
	// middleware only reflects Access-Control-Allow-Origin for an allowed
	// origin), so this combination is always a misconfiguration.
	if c.CORS.AllowCredentials && len(c.CORS.AllowedOrigins) == 0 {
		return fmt.Errorf(
			"CORS_ALLOW_CREDENTIALS=true requires a non-empty CORS_ALLOWED_ORIGINS: " +
				"credentialed CORS with no allowed origins can never succeed",
		)
	}

	// Enabling telemetry without an endpoint would build an exporter with
	// nowhere to send to. Fail fast naming the variable rather than boot into a
	// broken pipeline. An unset OTEL_ENABLED cannot reach here (it defaults to
	// endpoint-present), so this only fires on an explicit OTEL_ENABLED=true.
	if c.Observability.OTELEnabled && c.Observability.OTELExporterEndpoint == "" {
		return fmt.Errorf(
			"OTEL_ENABLED=true requires OTEL_EXPORTER_OTLP_ENDPOINT to be set " +
				"(the OTLP Collector endpoint, e.g. http://otel-collector:4318)",
		)
	}

	// OIDC authentication, when enabled, needs a discovery issuer AND an audience.
	// The issuer is where go-oidc fetches the JWKS and is matched exactly; the
	// audience is passed to the verifier as ClientID so the aud claim is checked
	// (ASVS V9.2.3 / V10.3.1). It must be THIS API's own audience identifier, not
	// a browser client_id (see the OIDCConfig.Audience doc for the token-type
	// confusion this avoids). We require the audience rather than let it be empty
	// because an empty ClientID forces go-oidc's SkipClientIDCheck, accepting
	// tokens minted for any audience, which this template never does.
	if c.Auth.OIDC.Enabled {
		if c.Auth.OIDC.IssuerURL == "" {
			return fmt.Errorf(
				"OIDC_ENABLED=true requires OIDC_ISSUER_URL to be set " +
					"(the OIDC issuer / discovery base, e.g. https://accounts.example.com)",
			)
		}
		if c.Auth.OIDC.Audience == "" {
			return fmt.Errorf(
				"OIDC_ENABLED=true requires OIDC_AUDIENCE to be set " +
					"(this API's own resource/audience identifier stamped into ACCESS tokens, " +
					"NOT a browser client_id; an empty audience would disable aud validation)",
			)
		}
	}

	// OPA authorisation, when enabled, needs a sidecar URL and a bounded timeout.
	// The timeout sits on every request's hot path, so a zero or absurd value is a
	// misconfiguration: fail fast rather than hang or spin. 60s is a generous
	// upper bound: a real OPA answers in single-digit milliseconds.
	if c.Auth.OPA.Enabled {
		if c.Auth.OPA.URL == "" {
			return fmt.Errorf(
				"OPA_ENABLED=true requires OPA_URL to be set " +
					"(the OPA sidecar base URL, e.g. http://opa:8181)",
			)
		}
		if c.Auth.OPA.DecisionPath == "" {
			return fmt.Errorf(
				"OPA_ENABLED=true requires OPA_DECISION_PATH to be set " +
					"(the REST Data API path to the boolean decision, e.g. v1/data/api/authz/allow)",
			)
		}
		if c.Auth.OPA.Timeout <= 0 || c.Auth.OPA.Timeout > 60*time.Second {
			return fmt.Errorf(
				"OPA_TIMEOUT_MS must be in (0, 60000], got %d ms",
				c.Auth.OPA.Timeout.Milliseconds(),
			)
		}
	}

	return nil
}

// ConnectionString returns a properly-escaped PostgreSQL URL DSN.
// Using URL form via net/url ensures the user and password are percent-encoded
// so values containing special characters (spaces, quotes, '@', ':') are
// transmitted safely. The result is consumable by pgxpool.ParseConfig.
func (c *DatabaseConfig) ConnectionString() string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.User, c.Password),
		Host:   c.Host + ":" + c.Port,
		Path:   "/" + c.Name,
	}
	q := u.Query()
	q.Set("sslmode", c.SSLMode)
	u.RawQuery = q.Encode()
	return u.String()
}

func parseOrigins(raw string) []string {
	var origins []string
	for o := range strings.SplitSeq(raw, ",") {
		if trimmed := strings.TrimSpace(o); trimmed != "" {
			origins = append(origins, trimmed)
		}
	}
	return origins
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvInt parses an integer environment variable. Returns an error rather
// than silently falling back when the value is set but unparseable: a typo
// in DB_MAX_OPEN_CONNS should not silently boot with the default.
func getEnvInt(key string, defaultValue int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue, nil
	}
	intValue, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid integer %q", key, value)
	}
	return intValue, nil
}

func getEnvBool(key string, defaultValue bool) (bool, error) {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue, nil
	}
	boolValue, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s: invalid boolean %q", key, value)
	}
	return boolValue, nil
}
