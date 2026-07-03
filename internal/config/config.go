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
	Server   ServerConfig
	Database DatabaseConfig
	Log      LogConfig
	CORS     CORSConfig
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
	// default) disables the limiter entirely — the middleware is not even
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
	Host            string
	Port            string
	User            string
	Password        string
	Name            string
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
	AllowDestructive string // MIGRATOR_ALLOW_DESTRUCTIVE — "true" gates destructive subcommands (paired with --confirm-destructive)
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
		return nil, fmt.Errorf("MIGRATOR_LOCK_TIMEOUT %q is not a valid Postgres duration (e.g. 5s, 100ms, 1min, 2h)", cfg.LockTimeout)
	}
	if !isPostgresDuration(cfg.StatementTimeout) {
		return nil, fmt.Errorf("MIGRATOR_STATEMENT_TIMEOUT %q is not a valid Postgres duration (e.g. 5s, 100ms, 1min, 2h)", cfg.StatementTimeout)
	}
	return cfg, nil
}

// postgresDurationRE matches the Postgres duration strings the migrator
// accepts: an integer optionally followed by one of the standard units.
// Postgres also accepts plain integers (interpreted as ms for lock_timeout
// and statement_timeout), so the unit is optional.
var postgresDurationRE = regexp.MustCompile(`^\s*[0-9]+\s*(us|ms|s|min|h|d)?\s*$`)

func isPostgresDuration(s string) bool {
	return postgresDurationRE.MatchString(s)
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
			// Default to production (info level) — a fail-safe default so an
			// unset LOG_LEVEL never accidentally ships debug-level logs (which
			// can be verbose and may surface internals). Dev sets it explicitly.
			Level: strings.ToLower(getEnv("LOG_LEVEL", "production")),
		},
		CORS: CORSConfig{
			AllowedOrigins:   parseOrigins(getEnv("CORS_ALLOWED_ORIGINS", "")),
			AllowCredentials: allowCreds,
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
	// A WriteTimeout shorter than RequestTimeout causes slow responses to be
	// truncated mid-write before the handler timeout fires — usually surfacing
	// as confusing "connection reset" reports. Fail at startup rather than ship.
	if c.Server.WriteTimeout <= c.Server.RequestTimeout {
		return fmt.Errorf(
			"SERVER_WRITE_TIMEOUT_SECS (%v) must be greater than SERVER_REQUEST_TIMEOUT_SECS (%v) "+
				"so the handler timeout fires before the write window closes",
			c.Server.WriteTimeout, c.Server.RequestTimeout,
		)
	}

	// The CORS middleware matches origins exactly (slices.Contains), so a
	// wildcard entry is silently inert — it would never match and contradicts
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
			"CORS_ALLOW_CREDENTIALS=true requires a non-empty CORS_ALLOWED_ORIGINS — " +
				"credentialed CORS with no allowed origins can never succeed",
		)
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
// than silently falling back when the value is set but unparseable — a typo
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
