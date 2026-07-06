// Package db wraps the pgx connection pool behind the PgxIface abstraction and
// provides the WithTransaction helper, so repositories run the same code inside
// and outside a transaction.
package db

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sud0x0/go-api-template/internal/config"
	"github.com/sud0x0/go-api-template/internal/shared/logger"
)

// PgxIface is the minimal pgx interface needed by repositories. It is
// satisfied by *pgxpool.Pool, pgx.Tx, and the pgxmock mock pool, so
// repositories work both inside and outside transactions and are unit-testable
// without a real database.
type PgxIface interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// DB wraps a pgxpool.Pool and provides application-level helpers.
// Obtain one via New() and pass it explicitly to repositories: no globals.
type DB struct {
	pool *pgxpool.Pool
}

// New opens and validates a Postgres connection pool using the provided configuration.
// Returns an error for any misconfiguration rather than calling os.Exit:
// the caller (main) decides whether to exit and how to log the failure.
//
// tracer is wired into the pool's ConnConfig so every query the application
// runs is instrumented. Pass nil for an uninstrumented pool (useful in tests
// that do not care about query metrics).
//
// requiredTables lists the tables that must exist for the application to run;
// New refuses to return a pool if any are missing. main aggregates these from
// each feature package (e.g. userlog.RequiredTables) so this core package never
// has to be edited when a feature is added. Pass nil to skip the check.
func New(cfg *config.DatabaseConfig, log logger.Logger, tracer pgx.QueryTracer, requiredTables []string) (*DB, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.ConnectionString())
	if err != nil {
		return nil, fmt.Errorf("parse pool config: %w", err)
	}

	maxConns, err := toInt32(cfg.MaxOpenConns, "MaxOpenConns")
	if err != nil {
		return nil, fmt.Errorf("pool config: %w", err)
	}
	minConns, err := toInt32(cfg.MinConns, "MinConns")
	if err != nil {
		return nil, fmt.Errorf("pool config: %w", err)
	}
	poolCfg.MaxConns = maxConns
	poolCfg.MinConns = minConns
	poolCfg.MaxConnLifetime = cfg.ConnMaxLifetime
	poolCfg.MaxConnIdleTime = cfg.ConnMaxIdleTime

	if tracer != nil {
		poolCfg.ConnConfig.Tracer = tracer
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	if err := verifySchema(ctx, pool, requiredTables); err != nil {
		pool.Close()
		return nil, fmt.Errorf("schema verification failed, run make db-migrate: %w", err)
	}

	log.LogInfo("database initialised", "host", cfg.Host, "dbname", cfg.Name)
	return &DB{pool: pool}, nil
}

// toInt32 converts a pool-size config value with an explicit range check.
// config.Load validates these at startup; this guard additionally protects
// direct constructions of DatabaseConfig (tests, future workers/CLIs) that
// do not pass through Load, the same defence-in-depth reasoning as
// repoMaxUserIDLength. The explicit bounds also make the conversion provably
// safe to static analysers (CodeQL go/incorrect-integer-conversion, gosec G115).
//
// This guards ONLY the conversion: the representable [0, MaxInt32] range. The
// SEMANTIC rules (MaxOpenConns >= 1, MinConns <= MaxOpenConns) live in
// config.validate() and must NOT be duplicated here: do not "complete" the
// lower bound to 1 or add a relational check, or the two layers drift.
func toInt32(n int, name string) (int32, error) {
	if n < 0 || n > math.MaxInt32 {
		return 0, fmt.Errorf("%s out of range [0, %d]: %d", name, math.MaxInt32, n)
	}
	return int32(n), nil
}

// Pool returns the underlying *pgxpool.Pool. Repositories accept the PgxIface
// interface and *pgxpool.Pool satisfies it.
func (d *DB) Pool() *pgxpool.Pool {
	return d.pool
}

// Close closes the database pool. pgxpool.Pool.Close() does not return an
// error, so this method does not either.
func (d *DB) Close() {
	if d.pool != nil {
		d.pool.Close()
	}
}

// HealthCheck performs a liveness check on the database connection.
func (d *DB) HealthCheck(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if err := d.pool.Ping(ctx); err != nil {
		return fmt.Errorf("database health check failed: %w", err)
	}
	return nil
}

// WithTransaction executes fn inside a database transaction.
// Rolls back automatically if fn returns an error; commits otherwise.
func (d *DB) WithTransaction(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			return fmt.Errorf("tx error: %w, rollback error: %v", err, rbErr)
		}
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// verifySchema checks that every table in requiredTables exists in the public
// schema, returning an error naming the missing ones. The list is supplied by
// the caller (aggregated from feature packages in main) rather than hard-coded
// here, so adding a feature never edits this core package. An empty/nil list
// makes this a no-op.
func verifySchema(ctx context.Context, pool *pgxpool.Pool, requiredTables []string) error {
	query := `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name = $1
	`
	var missing []string
	for _, table := range requiredTables {
		var tableName string
		err := pool.QueryRow(ctx, query, table).Scan(&tableName)
		if errors.Is(err, pgx.ErrNoRows) {
			missing = append(missing, table)
		} else if err != nil {
			return fmt.Errorf("check table %s: %w", table, err)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required tables: %v", missing)
	}
	return nil
}
