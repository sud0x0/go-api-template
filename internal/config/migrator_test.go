package config

import (
	"strings"
	"testing"
)

func setMigratorEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DB_HOST", "db")
	t.Setenv("DB_PORT", "5432")
	t.Setenv("DB_USER", "u")
	t.Setenv("DB_PASSWORD", "p")
	t.Setenv("DB_NAME", "d")
}

func TestLoadMigrator_DefaultsAreValid(t *testing.T) {
	setMigratorEnv(t)
	t.Setenv("MIGRATOR_LOCK_TIMEOUT", "")
	t.Setenv("MIGRATOR_STATEMENT_TIMEOUT", "")

	cfg, err := LoadMigrator()
	if err != nil {
		t.Fatalf("LoadMigrator: %v", err)
	}
	if cfg.LockTimeout != "5s" {
		t.Errorf("LockTimeout default: got %q want 5s", cfg.LockTimeout)
	}
	if cfg.StatementTimeout != "10min" {
		t.Errorf("StatementTimeout default: got %q want 10min", cfg.StatementTimeout)
	}
}

func TestLoadMigrator_RejectsBadDurations(t *testing.T) {
	setMigratorEnv(t)
	cases := []struct {
		name string
		key  string
		bad  string
	}{
		{"lock garbage", "MIGRATOR_LOCK_TIMEOUT", "forever"},
		{"lock with leading word", "MIGRATOR_LOCK_TIMEOUT", "abc5s"},
		{"statement garbage", "MIGRATOR_STATEMENT_TIMEOUT", "5 seconds"},
		{"statement empty unit suffix", "MIGRATOR_STATEMENT_TIMEOUT", "5xyz"},
		// Zero means "disabled / wait forever" in Postgres — rejected so the
		// fail-fast timeouts can't be silently switched off.
		{"lock zero unitless", "MIGRATOR_LOCK_TIMEOUT", "0"},
		{"lock zero with unit", "MIGRATOR_LOCK_TIMEOUT", "0s"},
		{"statement zero", "MIGRATOR_STATEMENT_TIMEOUT", "0"},
		{"statement zero minutes", "MIGRATOR_STATEMENT_TIMEOUT", "0min"},
		// A bare integer is ambiguous (Postgres reads it as ms) — a unit is required.
		{"lock bare integer", "MIGRATOR_LOCK_TIMEOUT", "5"},
		{"statement bare integer", "MIGRATOR_STATEMENT_TIMEOUT", "5000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("MIGRATOR_LOCK_TIMEOUT", "5s")
			t.Setenv("MIGRATOR_STATEMENT_TIMEOUT", "10min")
			t.Setenv(c.key, c.bad)
			_, err := LoadMigrator()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), c.key) {
				t.Errorf("error should name the env var, got %v", err)
			}
		})
	}
}

func TestLoadMigrator_AcceptsValidDurations(t *testing.T) {
	setMigratorEnv(t)
	// Every accepted value is a POSITIVE integer with an EXPLICIT unit.
	for _, d := range []string{"5s", "100ms", "1min", "2h", "1d", "500us", "  5s  "} {
		t.Run(d, func(t *testing.T) {
			t.Setenv("MIGRATOR_LOCK_TIMEOUT", d)
			t.Setenv("MIGRATOR_STATEMENT_TIMEOUT", d)
			if _, err := LoadMigrator(); err != nil {
				t.Errorf("LoadMigrator(%q): %v", d, err)
			}
		})
	}
}

func TestIsPostgresDuration(t *testing.T) {
	// Positive integer + explicit unit only.
	good := []string{"5s", "100ms", "1min", "2h", "1d", "500us", "  5s  "}
	// Rejected: empty, garbage, unknown/absent unit, non-integer, negative, AND —
	// the fix — a bare integer ("5"/"100", ambiguous ms) or any zero ("0"/"0s",
	// which means "disabled / wait forever" in Postgres).
	bad := []string{"", "forever", "abc5s", "5 seconds", "5xyz", "1.5s", "-5s", "0", "0s", "00min", "5", "100", "5000"}
	for _, s := range good {
		if !isPostgresDuration(s) {
			t.Errorf("isPostgresDuration(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if isPostgresDuration(s) {
			t.Errorf("isPostgresDuration(%q) = true, want false", s)
		}
	}
}
