// Package migrate hosts the testable pieces of the production migrator
// binary (cmd/migrate): the destructive-command classifier, the destructive
// gate, and the pgx connection config builder. The cmd/migrate package
// itself contains only entrypoint wiring and is intentionally thin.
package migrate

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/sud0x0/go-api-template/internal/config"
)

// DestructiveFlag is the CLI flag name that gates destructive commands.
const DestructiveFlag = "confirm-destructive"

// DestructiveEnv is the env var name that gates destructive commands.
const DestructiveEnv = "MIGRATOR_ALLOW_DESTRUCTIVE"

// SupportedCommands lists every subcommand the migrator accepts. Used by
// main for argument validation and by tests for exhaustive classification.
var SupportedCommands = []string{
	"up",
	"up-by-one",
	"up-to",
	"down",
	"down-to",
	"redo",
	"reset",
	"status",
	"db-version",
}

// IsSupported reports whether cmd is a recognised subcommand.
func IsSupported(cmd string) bool {
	return slices.Contains(SupportedCommands, cmd)
}

// IsDestructive reports whether the command can lose data. Up-only
// commands (up, up-by-one, up-to) and read-only commands (status,
// db-version) are NOT destructive even though up commands alter the
// schema — a forward migration is recoverable; a backward one is not.
func IsDestructive(cmd string) bool {
	switch cmd {
	case "down", "down-to", "redo", "reset":
		return true
	}
	return false
}

// CheckDestructiveGate returns nil if the command is non-destructive OR
// both gates are satisfied. Otherwise it returns a descriptive error
// listing exactly which gates were missing — the migrator main loop
// surfaces this and exits 2.
//
// Both gates are required deliberately: the env var alone would let a
// shell that has the env exported run destructive commands by accident;
// the flag alone would let a typo'd "destructive=true" alias slip through
// in a CI workflow. Combining them requires the operator to acknowledge
// the action in two independent channels.
func CheckDestructiveGate(cmd string, confirmFlag bool, envValue string) error {
	if !IsDestructive(cmd) {
		return nil
	}
	var missing []string
	if !confirmFlag {
		missing = append(missing, "--"+DestructiveFlag+" flag")
	}
	if envValue != "true" {
		missing = append(missing, DestructiveEnv+"=true env var")
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf(
		"destructive command %q refused — missing: %s",
		cmd, strings.Join(missing, " AND "),
	)
}

// BuildConnConfig produces a *pgx.ConnConfig suitable for the migrator's
// short-lived *sql.DB. The migrator pins lock_timeout and statement_timeout
// as Postgres runtime parameters so a long-blocking lock acquisition fails
// fast rather than queueing behind production traffic and blocking writes.
//
// The result is suitable for stdlib.OpenDB(*cfg). Exported so tests can
// verify the runtime parameters are present without opening a connection.
func BuildConnConfig(cfg *config.MigratorConfig) (*pgx.ConnConfig, error) {
	connCfg, err := pgx.ParseConfig(cfg.Database.ConnectionString())
	if err != nil {
		return nil, fmt.Errorf("parse db config: %w", err)
	}
	if connCfg.RuntimeParams == nil {
		connCfg.RuntimeParams = map[string]string{}
	}
	connCfg.RuntimeParams["lock_timeout"] = cfg.LockTimeout
	connCfg.RuntimeParams["statement_timeout"] = cfg.StatementTimeout
	return connCfg, nil
}

// OpenDB opens a *sql.DB for the migrator with timeouts wired through
// pgx runtime parameters. Goose requires a *sql.DB; the migrator is the
// ONLY part of this codebase that uses database/sql + pgx/v5/stdlib —
// the API uses native pgxpool.
func OpenDB(cfg *config.MigratorConfig) (*sql.DB, error) {
	connCfg, err := BuildConnConfig(cfg)
	if err != nil {
		return nil, err
	}
	return stdlib.OpenDB(*connCfg), nil
}
