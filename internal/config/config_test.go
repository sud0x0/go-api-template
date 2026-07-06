package config

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestConnectionString_EscapesSpecialCharacters (item 18) verifies that a
// password containing characters that would break the keyword=value form
// (space, quote, @) is correctly percent-encoded in the URL form, and that
// the encoded URL parses back to the original password.
func TestConnectionString_EscapesSpecialCharacters(t *testing.T) {
	cfg := &DatabaseConfig{
		Host:     "db.example.com",
		Port:     "5432",
		User:     "appuser",
		Password: `p ss"w@rd`,
		Name:     "appdb",
		SSLMode:  "require",
	}

	dsn := cfg.ConnectionString()

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("DSN does not round-trip through url.Parse: %v\ndsn=%s", err, dsn)
	}

	if u.Scheme != "postgres" {
		t.Errorf("scheme: got %q want postgres", u.Scheme)
	}
	if u.Host != "db.example.com:5432" {
		t.Errorf("host: got %q want db.example.com:5432", u.Host)
	}

	gotUser := u.User.Username()
	if gotUser != "appuser" {
		t.Errorf("user: got %q want appuser", gotUser)
	}
	gotPass, _ := u.User.Password()
	if gotPass != `p ss"w@rd` {
		t.Errorf("password round-trip failed: got %q want %q", gotPass, `p ss"w@rd`)
	}
	if u.Path != "/appdb" {
		t.Errorf("path: got %q want /appdb", u.Path)
	}
	if u.Query().Get("sslmode") != "require" {
		t.Errorf("sslmode: got %q want require", u.Query().Get("sslmode"))
	}
}

// TestGetEnvInt_FailsOnInvalid (item 19) verifies that a malformed numeric
// env var surfaces a descriptive error instead of silently falling back.
func TestGetEnvInt_FailsOnInvalid(t *testing.T) {
	t.Setenv("DB_MAX_OPEN_CONNS", "not-a-number")

	_, err := getEnvInt("DB_MAX_OPEN_CONNS", 100)
	if err == nil {
		t.Fatal("expected error for malformed int, got nil")
	}
	if !strings.Contains(err.Error(), "DB_MAX_OPEN_CONNS") {
		t.Errorf("error should mention the env key, got: %v", err)
	}
	if !strings.Contains(err.Error(), "not-a-number") {
		t.Errorf("error should quote the bad value, got: %v", err)
	}
}

func TestGetEnvInt_UsesDefaultWhenUnset(t *testing.T) {
	t.Setenv("UNSET_TEST_VAR_FOR_DEFAULT", "")

	v, err := getEnvInt("UNSET_TEST_VAR_FOR_DEFAULT", 42)
	if err != nil {
		t.Fatalf("unset key should use default, got error: %v", err)
	}
	if v != 42 {
		t.Errorf("got %d want 42", v)
	}
}

func TestGetEnvInt_ParsesValidValue(t *testing.T) {
	t.Setenv("DB_MAX_OPEN_CONNS", "250")

	v, err := getEnvInt("DB_MAX_OPEN_CONNS", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 250 {
		t.Errorf("got %d want 250", v)
	}
}

func TestLoad_FailsOnMalformedInt(t *testing.T) {
	t.Setenv("DB_HOST", "db")
	t.Setenv("DB_PORT", "5432")
	t.Setenv("DB_USER", "u")
	t.Setenv("DB_PASSWORD", "p")
	t.Setenv("DB_NAME", "d")
	t.Setenv("DB_MAX_OPEN_CONNS", "twenty")

	if _, err := Load(); err == nil {
		t.Fatal("expected Load() to fail on malformed DB_MAX_OPEN_CONNS, got nil")
	}
}

// setRequiredDBEnv sets the minimum env vars Load() needs to succeed so a test
// can focus on the one variable it is exercising.
func setRequiredDBEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DB_HOST", "db")
	t.Setenv("DB_PORT", "5432")
	t.Setenv("DB_USER", "u")
	t.Setenv("DB_PASSWORD", "p")
	t.Setenv("DB_NAME", "d")
}

// TestLoad_TrustProxyHeaders covers the opt-in proxy-trust flag: disabled by
// default, parsed when set, rejected at startup when malformed.
func TestLoad_TrustProxyHeaders(t *testing.T) {
	t.Run("default false", func(t *testing.T) {
		setRequiredDBEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Server.TrustProxyHeaders {
			t.Error("TRUST_PROXY_HEADERS should default to false")
		}
	})

	t.Run("parsed when set", func(t *testing.T) {
		setRequiredDBEnv(t)
		t.Setenv("TRUST_PROXY_HEADERS", "true")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !cfg.Server.TrustProxyHeaders {
			t.Error("TRUST_PROXY_HEADERS=true should parse to true")
		}
	})

	t.Run("invalid rejected", func(t *testing.T) {
		setRequiredDBEnv(t)
		t.Setenv("TRUST_PROXY_HEADERS", "maybe")
		if _, err := Load(); err == nil {
			t.Fatal("expected Load() to fail on malformed TRUST_PROXY_HEADERS, got nil")
		}
	})
}

// TestLoad_RateLimitRPM covers the opt-in app-level rate limiter config:
// disabled (0) by default, a positive value parsed, and invalid values
// (negative or non-integer) rejected at startup.
func TestLoad_RateLimitRPM(t *testing.T) {
	t.Run("disabled by default", func(t *testing.T) {
		setRequiredDBEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Server.RateLimitRPM != 0 {
			t.Errorf("RATE_LIMIT_RPM default: got %d want 0 (disabled)", cfg.Server.RateLimitRPM)
		}
	})

	t.Run("positive value parsed", func(t *testing.T) {
		setRequiredDBEnv(t)
		t.Setenv("RATE_LIMIT_RPM", "120")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Server.RateLimitRPM != 120 {
			t.Errorf("got %d want 120", cfg.Server.RateLimitRPM)
		}
	})

	t.Run("negative rejected", func(t *testing.T) {
		setRequiredDBEnv(t)
		t.Setenv("RATE_LIMIT_RPM", "-5")
		if _, err := Load(); err == nil {
			t.Fatal("expected Load() to reject negative RATE_LIMIT_RPM, got nil")
		}
	})

	t.Run("non-integer rejected", func(t *testing.T) {
		setRequiredDBEnv(t)
		t.Setenv("RATE_LIMIT_RPM", "lots")
		if _, err := Load(); err == nil {
			t.Fatal("expected Load() to reject non-integer RATE_LIMIT_RPM, got nil")
		}
	})
}

// TestLoad_PublicReadiness covers the readiness-exposure flag: default true
// (backwards compatible), parsed when set false, rejected when malformed.
func TestLoad_PublicReadiness(t *testing.T) {
	t.Run("default true", func(t *testing.T) {
		setRequiredDBEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !cfg.Server.PublicReadiness {
			t.Error("PUBLIC_READINESS should default to true")
		}
	})

	t.Run("parsed false", func(t *testing.T) {
		setRequiredDBEnv(t)
		t.Setenv("PUBLIC_READINESS", "false")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Server.PublicReadiness {
			t.Error("PUBLIC_READINESS=false should parse to false")
		}
	})

	t.Run("invalid rejected", func(t *testing.T) {
		setRequiredDBEnv(t)
		t.Setenv("PUBLIC_READINESS", "sometimes")
		if _, err := Load(); err == nil {
			t.Fatal("expected Load() to reject malformed PUBLIC_READINESS, got nil")
		}
	})
}

// TestLoad_CORSValidation covers the startup CORS guards: a wildcard origin is
// rejected (it is silently inert in the middleware), and credentialed CORS with
// no origins is rejected (it can never succeed). A valid explicit-origin config
// passes.
func TestLoad_CORSValidation(t *testing.T) {
	t.Run("wildcard origin rejected", func(t *testing.T) {
		setRequiredDBEnv(t)
		t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example.com,*")
		_, err := Load()
		if err == nil {
			t.Fatal("expected Load() to reject a wildcard origin, got nil")
		}
		if !strings.Contains(err.Error(), "CORS_ALLOWED_ORIGINS") {
			t.Errorf("error should mention CORS_ALLOWED_ORIGINS, got: %v", err)
		}
	})

	t.Run("subdomain wildcard rejected", func(t *testing.T) {
		setRequiredDBEnv(t)
		t.Setenv("CORS_ALLOWED_ORIGINS", "https://*.example.com")
		if _, err := Load(); err == nil {
			t.Fatal("expected Load() to reject a subdomain wildcard, got nil")
		}
	})

	t.Run("credentials with no origins rejected", func(t *testing.T) {
		setRequiredDBEnv(t)
		t.Setenv("CORS_ALLOW_CREDENTIALS", "true")
		// CORS_ALLOWED_ORIGINS unset → empty.
		_, err := Load()
		if err == nil {
			t.Fatal("expected Load() to reject credentials with empty origins, got nil")
		}
		if !strings.Contains(err.Error(), "CORS_ALLOW_CREDENTIALS") {
			t.Errorf("error should mention CORS_ALLOW_CREDENTIALS, got: %v", err)
		}
	})

	t.Run("valid explicit origins pass", func(t *testing.T) {
		setRequiredDBEnv(t)
		t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example.com,https://admin.example.com")
		t.Setenv("CORS_ALLOW_CREDENTIALS", "true")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("valid CORS config should load, got: %v", err)
		}
		if len(cfg.CORS.AllowedOrigins) != 2 {
			t.Errorf("got %d origins, want 2", len(cfg.CORS.AllowedOrigins))
		}
	})
}

// TestLoad_FailsOnInvertedTimeouts (item 7A) verifies that the validation in
// Config.validate() rejects a WriteTimeout shorter than RequestTimeout —
// otherwise slow responses get truncated mid-write before the handler
// timeout fires.
func TestLoad_FailsOnInvertedTimeouts(t *testing.T) {
	t.Setenv("DB_HOST", "db")
	t.Setenv("DB_PORT", "5432")
	t.Setenv("DB_USER", "u")
	t.Setenv("DB_PASSWORD", "p")
	t.Setenv("DB_NAME", "d")
	t.Setenv("SERVER_WRITE_TIMEOUT_SECS", "30")
	t.Setenv("SERVER_REQUEST_TIMEOUT_SECS", "60")

	_, err := Load()
	if err == nil {
		t.Fatal("expected Load() to fail when WriteTimeout < RequestTimeout, got nil")
	}
	if !strings.Contains(err.Error(), "WRITE_TIMEOUT") || !strings.Contains(err.Error(), "REQUEST_TIMEOUT") {
		t.Errorf("error should name both timeouts, got: %v", err)
	}
}

// TestLoad_AuthConfig covers the two-layer auth config: both layers off by
// default, each derives Enabled from its endpoint, and the fail-fast rules name
// the offending variable.
func TestLoad_AuthConfig(t *testing.T) {
	t.Run("both disabled by default", func(t *testing.T) {
		setRequiredDBEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Auth.OIDC.Enabled {
			t.Error("OIDC should default to disabled when OIDC_ISSUER_URL is unset")
		}
		if cfg.Auth.OPA.Enabled {
			t.Error("OPA should default to disabled when OPA_URL is unset")
		}
		// Defaults for the OPA knobs are still populated even while disabled.
		if cfg.Auth.OPA.DecisionPath != "v1/data/api/authz/allow" {
			t.Errorf("OPA_DECISION_PATH default: got %q", cfg.Auth.OPA.DecisionPath)
		}
		if cfg.Auth.OPA.Timeout != 100*time.Millisecond {
			t.Errorf("OPA_TIMEOUT_MS default: got %v, want 100ms", cfg.Auth.OPA.Timeout)
		}
	})

	t.Run("OIDC enabled by issuer requires audience", func(t *testing.T) {
		setRequiredDBEnv(t)
		t.Setenv("OIDC_ISSUER_URL", "https://accounts.example.com")
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "OIDC_AUDIENCE") {
			t.Fatalf("expected failure naming OIDC_AUDIENCE, got: %v", err)
		}
	})

	t.Run("OIDC enabled with issuer and audience", func(t *testing.T) {
		setRequiredDBEnv(t)
		t.Setenv("OIDC_ISSUER_URL", "https://accounts.example.com")
		t.Setenv("OIDC_AUDIENCE", "go-api")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !cfg.Auth.OIDC.Enabled {
			t.Error("OIDC should be enabled when an issuer is set")
		}
	})

	t.Run("OIDC_ENABLED=true without issuer fails", func(t *testing.T) {
		setRequiredDBEnv(t)
		t.Setenv("OIDC_ENABLED", "true")
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "OIDC_ISSUER_URL") {
			t.Fatalf("expected failure naming OIDC_ISSUER_URL, got: %v", err)
		}
	})

	t.Run("OPA enabled by URL", func(t *testing.T) {
		setRequiredDBEnv(t)
		t.Setenv("OPA_URL", "http://opa:8181")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !cfg.Auth.OPA.Enabled {
			t.Error("OPA should be enabled when OPA_URL is set")
		}
	})

	t.Run("OPA_ENABLED=true without URL fails", func(t *testing.T) {
		setRequiredDBEnv(t)
		t.Setenv("OPA_ENABLED", "true")
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "OPA_URL") {
			t.Fatalf("expected failure naming OPA_URL, got: %v", err)
		}
	})

	t.Run("OPA timeout is bounded", func(t *testing.T) {
		for _, ms := range []string{"0", "60001"} {
			setRequiredDBEnv(t)
			t.Setenv("OPA_URL", "http://opa:8181")
			t.Setenv("OPA_TIMEOUT_MS", ms)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "OPA_TIMEOUT_MS") {
				t.Fatalf("OPA_TIMEOUT_MS=%s: expected failure naming OPA_TIMEOUT_MS, got: %v", ms, err)
			}
		}
	})
}

// TestLoadDatabase_PoolSizeBounds covers the startup bounds on the connection
// pool sizes: MaxOpenConns must be in [1, MaxInt32] and MinConns in
// [0, MaxOpenConns]. A value above MaxInt32 is rejected regardless of platform
// (on 64-bit int by validate(), on 32-bit int earlier by strconv.Atoi), so the
// case asserts only that LoadDatabase errors. The default pair (100/10) is the
// production config and must be accepted.
func TestLoadDatabase_PoolSizeBounds(t *testing.T) {
	cases := []struct {
		name     string
		maxOpen  string // "" = leave unset (use default 100)
		minConns string // "" = leave unset (use default 10)
		wantErr  bool
		errNames string // substring the error must mention (when wantErr)
	}{
		{name: "defaults accepted (100/10)", wantErr: false},
		{name: "explicit valid pair", maxOpen: "50", minConns: "5", wantErr: false},
		{name: "min may equal max", maxOpen: "20", minConns: "20", wantErr: false},
		{name: "min zero accepted", maxOpen: "20", minConns: "0", wantErr: false},
		{name: "max zero rejected", maxOpen: "0", minConns: "0", wantErr: true, errNames: "DB_MAX_OPEN_CONNS"},
		{name: "max above MaxInt32 rejected", maxOpen: "2147483648", minConns: "0", wantErr: true},
		{name: "min greater than max rejected", maxOpen: "5", minConns: "6", wantErr: true, errNames: "DB_MIN_CONNS"},
		{name: "min negative rejected", maxOpen: "5", minConns: "-1", wantErr: true, errNames: "DB_MIN_CONNS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredDBEnv(t)
			if tc.maxOpen != "" {
				t.Setenv("DB_MAX_OPEN_CONNS", tc.maxOpen)
			}
			if tc.minConns != "" {
				t.Setenv("DB_MIN_CONNS", tc.minConns)
			}

			_, err := LoadDatabase()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got nil")
				}
				if tc.errNames != "" && !strings.Contains(err.Error(), tc.errNames) {
					t.Errorf("error should name %q, got: %v", tc.errNames, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected success, got: %v", err)
			}
		})
	}
}
