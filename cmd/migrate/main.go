// Command go-api-migrator is the production migrator binary. It embeds
// every SQL migration file and applies them through goose's Provider API
// behind a Postgres advisory lock, so concurrent migrator invocations
// against the same database are safely serialised.
//
// Connection details come from the same DB_* env vars the API uses
// (DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, DB_NAME, DB_SSLMODE). Two
// migrator-specific env vars cap how long a single migration may block
// production traffic: MIGRATOR_LOCK_TIMEOUT (default 5s) and
// MIGRATOR_STATEMENT_TIMEOUT (default 10min).
//
// Destructive subcommands (down, down-to, redo, reset) require BOTH
// --confirm-destructive AND MIGRATOR_ALLOW_DESTRUCTIVE=true to proceed.
//
// Exit codes:
//
//	0: success
//	1: migration or database error
//	2: usage or configuration error
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/sud0x0/go-api-template/internal/config"
	"github.com/sud0x0/go-api-template/internal/migrate"
	"github.com/sud0x0/go-api-template/internal/version"
	"github.com/sud0x0/go-api-template/migrations"
)

const (
	exitOK    = 0
	exitDB    = 1
	exitUsage = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable core of main. It returns an exit code instead of
// calling os.Exit so callers (tests, main) decide the process outcome.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("go-api-migrator", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printUsage(stderr) }

	var (
		confirmDestructive bool
		showHelp           bool
		showVersion        bool
	)
	fs.BoolVar(&confirmDestructive, migrate.DestructiveFlag, false,
		"required (with "+migrate.DestructiveEnv+"=true) for destructive subcommands")
	fs.BoolVar(&showHelp, "help", false, "print usage and exit")
	fs.BoolVar(&showHelp, "h", false, "print usage and exit (shorthand)")
	fs.BoolVar(&showVersion, "version", false, "print version and exit")
	fs.BoolVar(&showVersion, "v", false, "print version and exit (shorthand)")

	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if showHelp {
		printUsage(stdout)
		return exitOK
	}
	if showVersion {
		_, _ = fmt.Fprint(stdout, version.Current().Banner("go-api-migrator"))
		return exitOK
	}

	positional := fs.Args()
	if len(positional) == 0 {
		_, _ = fmt.Fprintln(stderr, "missing subcommand. Try --help.")
		return exitUsage
	}

	// Bare-word aliases for ergonomics ("migrate help" / "migrate version").
	switch positional[0] {
	case "help":
		printUsage(stdout)
		return exitOK
	case "version":
		_, _ = fmt.Fprint(stdout, version.Current().Banner("go-api-migrator"))
		return exitOK
	}

	cmd := positional[0]
	cmdArgs := positional[1:]

	if !migrate.IsSupported(cmd) {
		_, _ = fmt.Fprintf(stderr, "unknown subcommand %q. Try --help.\n", cmd)
		return exitUsage
	}

	// Load config first: env reads (including MIGRATOR_ALLOW_DESTRUCTIVE)
	// live exclusively in internal/config. The destructive gate is checked
	// from cfg.AllowDestructive immediately afterward, before we open any
	// database connection.
	cfg, err := config.LoadMigrator()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "config error: %v\n", err)
		return exitUsage
	}

	if err := migrate.CheckDestructiveGate(cmd, confirmDestructive, cfg.AllowDestructive); err != nil {
		_, _ = fmt.Fprintln(stderr, err.Error())
		return exitUsage
	}

	db, err := migrate.OpenDB(cfg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "open database: %v\n", err)
		return exitDB
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// PostgresSessionLocker uses pg_advisory_lock so concurrent migrator
	// invocations against the same database serialise instead of racing.
	// IMPORTANT: advisory locks are PER SESSION; the migrator must connect
	// directly to Postgres (or via session-level pooling). They do not
	// survive PgBouncer transaction/statement pooling.
	sessionLocker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "session locker: %v\n", err)
		return exitDB
	}

	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		migrations.FS,
		goose.WithSessionLocker(sessionLocker),
	)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "build provider: %v\n", err)
		return exitDB
	}

	if err := dispatch(ctx, provider, cmd, cmdArgs, stdout); err != nil {
		_, _ = fmt.Fprintf(stderr, "migration error: %v\n", err)
		return exitDB
	}
	return exitOK
}

// dispatch runs the requested subcommand against the Provider and prints
// each MigrationResult / MigrationStatus to stdout.
func dispatch(ctx context.Context, p *goose.Provider, cmd string, args []string, stdout io.Writer) error {
	switch cmd {
	case "up":
		results, err := p.Up(ctx)
		printResults(stdout, results)
		return err
	case "up-by-one":
		result, err := p.UpByOne(ctx)
		if result != nil {
			printResults(stdout, []*goose.MigrationResult{result})
		}
		return err
	case "up-to":
		v, err := parseVersion(args)
		if err != nil {
			return err
		}
		results, err := p.UpTo(ctx, v)
		printResults(stdout, results)
		return err
	case "down":
		result, err := p.Down(ctx)
		if result != nil {
			printResults(stdout, []*goose.MigrationResult{result})
		}
		return err
	case "down-to":
		v, err := parseVersion(args)
		if err != nil {
			return err
		}
		results, err := p.DownTo(ctx, v)
		printResults(stdout, results)
		return err
	case "reset":
		// Spec: `reset` → DownTo(ctx, 0)
		results, err := p.DownTo(ctx, 0)
		printResults(stdout, results)
		return err
	case "redo":
		// Spec: `redo` = Down then UpByOne. The Provider has no native redo.
		down, err := p.Down(ctx)
		if down != nil {
			printResults(stdout, []*goose.MigrationResult{down})
		}
		if err != nil {
			return err
		}
		up, err := p.UpByOne(ctx)
		if up != nil {
			printResults(stdout, []*goose.MigrationResult{up})
		}
		return err
	case "status":
		statuses, err := p.Status(ctx)
		if err != nil {
			return err
		}
		printStatus(stdout, statuses)
		return nil
	case "db-version":
		v, err := p.GetDBVersion(ctx)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(stdout, v)
		return nil
	default:
		return fmt.Errorf("internal: unhandled subcommand %q", cmd)
	}
}

func parseVersion(args []string) (int64, error) {
	if len(args) < 1 {
		return 0, errors.New("missing version argument")
	}
	v, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid version %q: %w", args[0], err)
	}
	return v, nil
}

func printResults(w io.Writer, results []*goose.MigrationResult) {
	for _, r := range results {
		if r == nil {
			continue
		}
		dir := r.Direction
		name := ""
		if r.Source != nil {
			name = r.Source.Path
		}
		_, _ = fmt.Fprintf(w, "%-4s  v=%-6d  %-12s  %s\n",
			dir, sourceVersion(r.Source), r.Duration.Round(time.Millisecond), name)
	}
}

func sourceVersion(s *goose.Source) int64 {
	if s == nil {
		return 0
	}
	return s.Version
}

func printStatus(w io.Writer, statuses []*goose.MigrationStatus) {
	if len(statuses) == 0 {
		_, _ = fmt.Fprintln(w, "No migrations found.")
		return
	}
	const header = "Version    State      Applied At                     Name\n"
	_, _ = fmt.Fprint(w, header)
	_, _ = fmt.Fprintln(w, strings.Repeat("-", 80))
	for _, s := range statuses {
		applied := ""
		if !s.AppliedAt.IsZero() {
			applied = s.AppliedAt.Format(time.RFC3339)
		}
		name := ""
		if s.Source != nil {
			name = s.Source.Path
		}
		_, _ = fmt.Fprintf(w, "%-10d %-10s %-30s %s\n",
			sourceVersion(s.Source), s.State, applied, name)
	}
}

func printUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, usageText)
}

const usageText = `go-api-migrator: production database migrator for go-api-template.

USAGE:
  go-api-migrator [global flags] <subcommand> [args]

SUBCOMMANDS:
  up                Apply all pending migrations
  up-by-one         Apply the next pending migration
  up-to <version>   Apply migrations up to and including <version>
  down              Roll back the most recently applied migration   [destructive]
  down-to <version> Roll back to <version> (use 0 to roll back all) [destructive]
  redo              Roll back then re-apply the latest migration    [destructive]
  reset             Roll back every applied migration               [destructive]
  status            Print the version and applied/pending state of each migration
  db-version        Print the current schema version
  help              Print this usage
  version           Print build/version information

GLOBAL FLAGS:
  --confirm-destructive   Required (alongside MIGRATOR_ALLOW_DESTRUCTIVE=true) to run a destructive subcommand
  --help / -h             Print this usage
  --version / -v          Print build/version information

ENVIRONMENT:
  DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, DB_NAME, DB_SSLMODE
                                Standard database connection. Same vars as the API.
  MIGRATOR_LOCK_TIMEOUT         Postgres lock_timeout for the migration session (default 5s)
  MIGRATOR_STATEMENT_TIMEOUT    Postgres statement_timeout                       (default 10min)
  MIGRATOR_ALLOW_DESTRUCTIVE    Set to "true" alongside --confirm-destructive to enable destructive subcommands

EXIT CODES:
  0  success
  1  migration or database error
  2  usage or configuration error
`
