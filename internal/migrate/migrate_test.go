package migrate

import (
	"strings"
	"testing"

	"github.com/sud0x0/go-api-template/internal/config"
)

// TestIsSupported_ExhaustiveCoverage verifies every entry in
// SupportedCommands is reported supported, and unknown commands are not.
func TestIsSupported_ExhaustiveCoverage(t *testing.T) {
	for _, c := range SupportedCommands {
		if !IsSupported(c) {
			t.Errorf("IsSupported(%q): want true", c)
		}
	}
	for _, bad := range []string{"", "noop", "drop", "Up", "STATUS", "downto"} {
		if IsSupported(bad) {
			t.Errorf("IsSupported(%q): want false", bad)
		}
	}
}

// TestIsDestructive_Classification verifies each supported command's
// destructive classification, which the spec mandates as the source of
// truth for the gate.
func TestIsDestructive_Classification(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"up", false},
		{"up-by-one", false},
		{"up-to", false},
		{"status", false},
		{"db-version", false},
		{"down", true},
		{"down-to", true},
		{"redo", true},
		{"reset", true},
	}
	for _, c := range cases {
		if got := IsDestructive(c.cmd); got != c.want {
			t.Errorf("IsDestructive(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

func TestCheckDestructiveGate_NonDestructiveAlwaysPasses(t *testing.T) {
	for _, c := range []string{"up", "up-by-one", "up-to", "status", "db-version"} {
		// Even with both gates missing, non-destructive commands proceed.
		if err := CheckDestructiveGate(c, false, ""); err != nil {
			t.Errorf("CheckDestructiveGate(%q): got %v, want nil", c, err)
		}
	}
}

func TestCheckDestructiveGate_DestructiveRequiresBoth(t *testing.T) {
	cases := []struct {
		name       string
		flag       bool
		env        string
		wantOK     bool
		wantSubstr []string
	}{
		{"both missing", false, "", false, []string{"--confirm-destructive", "MIGRATOR_ALLOW_DESTRUCTIVE"}},
		{"flag only", true, "", false, []string{"MIGRATOR_ALLOW_DESTRUCTIVE"}},
		{"env only", false, "true", false, []string{"--confirm-destructive"}},
		{"env wrong value", true, "TRUE", false, []string{"MIGRATOR_ALLOW_DESTRUCTIVE"}},
		{"env yes is not true", true, "yes", false, []string{"MIGRATOR_ALLOW_DESTRUCTIVE"}},
		{"both present", true, "true", true, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckDestructiveGate("down", c.flag, c.env)
			if c.wantOK && err != nil {
				t.Fatalf("got %v, want nil", err)
			}
			if !c.wantOK {
				if err == nil {
					t.Fatal("got nil, want error")
				}
				for _, sub := range c.wantSubstr {
					if !strings.Contains(err.Error(), sub) {
						t.Errorf("error %q missing %q", err.Error(), sub)
					}
				}
			}
		})
	}
}

// TestBuildConnConfig_DefaultRuntimeParams (item 5) verifies that the
// pgx ConnConfig produced from a MigratorConfig has lock_timeout and
// statement_timeout populated with the defaults.
func TestBuildConnConfig_DefaultRuntimeParams(t *testing.T) {
	cfg := &config.MigratorConfig{
		Database: config.DatabaseConfig{
			Host: "h", Port: "5432", User: "u", Password: "p", Name: "d", SSLMode: "disable",
		},
		LockTimeout:      "5s",
		StatementTimeout: "10min",
	}
	cc, err := BuildConnConfig(cfg)
	if err != nil {
		t.Fatalf("BuildConnConfig: %v", err)
	}
	if cc.RuntimeParams["lock_timeout"] != "5s" {
		t.Errorf("lock_timeout: got %q want 5s", cc.RuntimeParams["lock_timeout"])
	}
	if cc.RuntimeParams["statement_timeout"] != "10min" {
		t.Errorf("statement_timeout: got %q want 10min", cc.RuntimeParams["statement_timeout"])
	}
}

// TestBuildConnConfig_RuntimeParamOverrides verifies non-default values
// pass through unchanged, important because long backfills routinely
// raise these.
func TestBuildConnConfig_RuntimeParamOverrides(t *testing.T) {
	cfg := &config.MigratorConfig{
		Database: config.DatabaseConfig{
			Host: "h", Port: "5432", User: "u", Password: "p", Name: "d", SSLMode: "disable",
		},
		LockTimeout:      "30s",
		StatementTimeout: "2h",
	}
	cc, err := BuildConnConfig(cfg)
	if err != nil {
		t.Fatalf("BuildConnConfig: %v", err)
	}
	if cc.RuntimeParams["lock_timeout"] != "30s" {
		t.Errorf("lock_timeout: got %q want 30s", cc.RuntimeParams["lock_timeout"])
	}
	if cc.RuntimeParams["statement_timeout"] != "2h" {
		t.Errorf("statement_timeout: got %q want 2h", cc.RuntimeParams["statement_timeout"])
	}
}
