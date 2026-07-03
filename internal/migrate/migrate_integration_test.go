//go:build integration

// Integration tests for the migrator. Run with: make test-integration
// (requires `make run` so the dev database is reachable on DB_HOST).
//
// These tests verify:
//  1. The embedded migrations apply cleanly against a real Postgres.
//  2. goose_db_version and the expected application tables exist after up.
//  3. The session locker actually serialises concurrent migrator runs —
//     two goroutines calling Up() at the same time must both return nil
//     errors AND each migration is recorded exactly once.

package migrate

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/sud0x0/go-api-template/migrations"
)

func mustEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("integration test requires %s (run `make run` first)", key)
	}
	return v
}

// openIntegrationDB opens a *sql.DB pointed at the compose dev database.
// Each test gets its own *sql.DB so concurrent-migrator tests can prove
// the advisory lock works across SESSIONS, not just across goroutines on
// one connection.
func openIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	host := mustEnv(t, "DB_HOST")
	port := mustEnv(t, "DB_PORT")
	user := mustEnv(t, "DB_USER")
	password := mustEnv(t, "DB_PASSWORD")
	name := mustEnv(t, "DB_NAME")
	sslmode := os.Getenv("DB_SSLMODE")
	if sslmode == "" {
		sslmode = "disable"
	}

	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   host + ":" + port,
		Path:   "/" + name,
	}
	q := u.Query()
	q.Set("sslmode", sslmode)
	u.RawQuery = q.Encode()

	connCfg, err := pgx.ParseConfig(u.String())
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	connCfg.RuntimeParams["lock_timeout"] = "5s"
	connCfg.RuntimeParams["statement_timeout"] = "30s"
	db := stdlib.OpenDB(*connCfg)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatalf("Ping: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newTestProvider(t *testing.T, db *sql.DB) *goose.Provider {
	t.Helper()
	sessionLocker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		t.Fatalf("NewPostgresSessionLocker: %v", err)
	}
	p, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		migrations.FS,
		goose.WithSessionLocker(sessionLocker),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return p
}

// resetSchema brings the database back to a known empty state so a test
// can drive Up() from zero. We don't use DownTo(0) here because that
// requires the goose_db_version table to exist, and a fresh test
// environment might not have it.
func resetSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, stmt := range []string{
		"DROP TABLE IF EXISTS logs CASCADE",
		"DROP TABLE IF EXISTS goose_db_version CASCADE",
		"DROP FUNCTION IF EXISTS update_updated_at_column() CASCADE",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("reset %q: %v", stmt, err)
		}
	}
}

// TestMigrator_UpAppliesSchema_Integration runs Up against an empty
// database and verifies the expected tables exist and Status is
// callable.
//
// The test resets the schema at the START (so it can drive Up from zero)
// but leaves migrations applied at the END. Tearing down on cleanup would
// drop the schema beneath any later test that expects it (e.g. userlog's
// CRUD integration test).
func TestMigrator_UpAppliesSchema_Integration(t *testing.T) {
	db := openIntegrationDB(t)
	resetSchema(t, db)

	p := newTestProvider(t, db)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	results, err := p.Up(ctx)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("Up returned no results — embedded migrations missing?")
	}

	// goose_db_version must exist.
	if !tableExists(t, db, "goose_db_version") {
		t.Error("goose_db_version table missing after Up")
	}
	// The application's logs table must exist (created by 00001).
	if !tableExists(t, db, "logs") {
		t.Error("logs table missing after Up")
	}

	// Status must run without error and return at least one row.
	statuses, err := p.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(statuses) == 0 {
		t.Error("Status returned no rows after Up")
	}
}

// TestMigrator_ConcurrentUpsSerialise_Integration verifies the session
// locker's promise: two migrators racing each other against the same
// database both complete without error, and every migration applies
// exactly once.
//
// The check is "exactly once" because without the lock, goose would
// attempt to re-apply already-applied migrations OR collide on the
// version table, surfacing as an error in at least one goroutine.
func TestMigrator_ConcurrentUpsSerialise_Integration(t *testing.T) {
	// Two separate *sql.DB instances so each goroutine has its own
	// connection pool — the lock has to do real work across sessions.
	dbA := openIntegrationDB(t)
	dbB := openIntegrationDB(t)
	resetSchema(t, dbA)
	// Intentionally no cleanup that drops the schema — see the comment on
	// TestMigrator_UpAppliesSchema_Integration. The test ends with
	// migrations applied so userlog's integration tests find their tables.

	pA := newTestProvider(t, dbA)
	pB := newTestProvider(t, dbB)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var (
		wg             sync.WaitGroup
		errA, errB     error
		resA, resB     []*goose.MigrationResult
		startedA       = make(chan struct{})
		startedB       = make(chan struct{})
		startBarrierA  = make(chan struct{})
		startBarrierB  = make(chan struct{})
		releaseBarrier = make(chan struct{})
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		close(startedA)
		<-startBarrierA
		resA, errA = pA.Up(ctx)
	}()
	go func() {
		defer wg.Done()
		close(startedB)
		<-startBarrierB
		resB, errB = pB.Up(ctx)
	}()

	// Wait until both goroutines are parked at the barrier, then release
	// them simultaneously so they race for the lock.
	<-startedA
	<-startedB
	close(startBarrierA)
	close(startBarrierB)
	close(releaseBarrier)

	wg.Wait()

	if errA != nil {
		t.Errorf("goroutine A returned error: %v", errA)
	}
	if errB != nil {
		t.Errorf("goroutine B returned error: %v", errB)
	}

	// Across both goroutines, every migration should be applied exactly
	// once. The second goroutine to acquire the lock sees the version
	// table already at HEAD and returns an empty slice.
	totalApplied := len(resA) + len(resB)
	statuses, err := pA.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	totalMigrations := len(statuses)
	if totalApplied != totalMigrations {
		t.Errorf("expected %d total migrations applied across both racers, got %d",
			totalMigrations, totalApplied)
	}
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	const q = `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = $1
		)`
	var exists bool
	if err := db.QueryRow(q, name).Scan(&exists); err != nil {
		t.Fatalf("tableExists(%q): %v", name, err)
	}
	return exists
}
