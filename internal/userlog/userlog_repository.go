package userlog

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sud0x0/go-api-template/internal/db"
)

// RequiredTables names the database tables this feature needs. cmd/api/main.go
// aggregates every feature's RequiredTables and passes them to db.New for
// startup schema verification — so adding a feature registers its tables here,
// never by editing internal/db.
var RequiredTables = []string{"logs"}

// repoMaxUserIDLength is a defence-in-depth bound on userID size to prevent
// pathological values from reaching the database.
//
// On the HTTP path this guard is now unreachable: shared.UserIDFromContext is
// the single enforcement point for the UUID user-ID contract and returns only
// canonical 36-char UUIDs, so the handler can never hand the repository an
// over-length string. The guard is retained because the repository is also
// callable directly by NON-handler paths (integration tests today, future
// workers/CLIs) that do not pass through UserIDFromContext and could supply an
// arbitrary string — those must still be bounded before they reach Postgres.
const repoMaxUserIDLength = 64

// SQL query strings.
//
// pgx prepares and caches these per connection automatically, so no manual
// prepared-statement bookkeeping is required.
const (
	queryGetLog = `SELECT id, user_id, date_and_time, log, created_at, updated_at FROM logs WHERE id = $1 AND user_id = $2`
	// queryGetLogs is the OFFSET-mode list. The ORDER BY includes id DESC (the
	// same deterministic order the keyset queries use) so both pagination modes
	// share one composite index (user_id, date_and_time DESC, id DESC) and pages
	// never shift ambiguously when two rows share a timestamp. OFFSET appears in
	// THIS query only; cursor-mode queries below are OFFSET-free by design.
	queryGetLogs = `SELECT id, user_id, date_and_time, log, created_at, updated_at FROM logs WHERE user_id = $1 AND date_and_time >= $2 AND date_and_time <= $3 ORDER BY date_and_time DESC, id DESC LIMIT $4 OFFSET $5`
	// queryGetLogsKeysetFirst is the FIRST page of a cursor walk (no cursor yet):
	// same filter and order as the offset query, no OFFSET.
	queryGetLogsKeysetFirst = `SELECT id, user_id, date_and_time, log, created_at, updated_at FROM logs WHERE user_id = $1 AND date_and_time >= $2 AND date_and_time <= $3 ORDER BY date_and_time DESC, id DESC LIMIT $4`
	// queryGetLogsKeysetAfter resumes AFTER a cursor. The row-value comparison
	// (date_and_time, id) < ($4, $5) matches the composite index and yields the
	// next page deterministically even across rows that share a timestamp.
	//
	// Verified with EXPLAIN (ANALYZE, BUFFERS) on 5000 seeded rows (Postgres 16):
	// "Index Scan using idx_logs_user_datetime_id" with the row-value comparison
	// as an Index Cond — `ROW(date_and_time, id) < ROW($4, $5)` — NOT a Filter,
	// alongside the user_id equality and the date-range bounds. The LIMIT stops
	// the scan after the page (≈5 buffer hits for 101 rows). A constant-cost
	// index seek; re-check the plan if the index or WHERE shape changes.
	queryGetLogsKeysetAfter = `SELECT id, user_id, date_and_time, log, created_at, updated_at FROM logs WHERE user_id = $1 AND date_and_time >= $2 AND date_and_time <= $3 AND (date_and_time, id) < ($4, $5) ORDER BY date_and_time DESC, id DESC LIMIT $6`
	queryCreateLog          = `INSERT INTO logs (user_id, date_and_time, log) VALUES ($1, $2, $3) RETURNING id, user_id, date_and_time, log, created_at, updated_at`
	// updated_at is maintained by the BEFORE UPDATE trigger (see migration 00001).
	// BEFORE triggers run before RETURNING is evaluated, so the returned row
	// carries the fresh trigger-set timestamp. Do not set updated_at here.
	queryUpdateLog = `UPDATE logs SET log = $1 WHERE id = $2 AND user_id = $3 RETURNING id, user_id, date_and_time, log, created_at, updated_at`
	queryDeleteLog = `DELETE FROM logs WHERE id = $1 AND user_id = $2`
)

// logRepository defines the interface for log data access.
type logRepository interface {
	getLog(ctx context.Context, id, userID string) (Log, error)
	getLogs(ctx context.Context, userID, startDate, endDate string, limit, offset int) ([]Log, error)
	getLogsKeysetFirst(ctx context.Context, userID, startDate, endDate string, limit int) ([]Log, error)
	getLogsKeysetAfter(ctx context.Context, userID, startDate, endDate string, cursorTime time.Time, cursorID string, limit int) ([]Log, error)
	createLog(ctx context.Context, userID, dateAndTime, logContent string) (Log, error)
	updateLog(ctx context.Context, id, userID, logContent string) (Log, error)
	deleteLog(ctx context.Context, id, userID string) error
}

// pgxLogRepository implements logRepository using native pgx.
type pgxLogRepository struct {
	pool db.PgxIface
}

// NewLogRepository creates a new logRepository backed by pgx.
// The PgxIface interface is satisfied by *pgxpool.Pool, pgx.Tx, and pgxmock
// mock pools, so the repository works inside or outside transactions and is
// unit-testable without a real database.
//
// The repository does not log — diagnostics are the handler's job (it owns the
// request-scoped logger and the bounded error metrics) — so no logger is taken.
func NewLogRepository(pool db.PgxIface) logRepository {
	return &pgxLogRepository{pool: pool}
}

func (r *pgxLogRepository) getLog(ctx context.Context, id, userID string) (Log, error) {
	if len(userID) > repoMaxUserIDLength {
		return Log{}, ErrInvalidInput
	}

	var entry Log
	err := r.pool.QueryRow(ctx, queryGetLog, id, userID).Scan(
		&entry.ID, &entry.UserID, &entry.DateAndTime, &entry.Log,
		&entry.CreatedAt, &entry.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Log{}, ErrLogNotFound
		}
		return Log{}, fmt.Errorf("getLog %s: %w: %w", id, ErrDatabase, err)
	}

	if err := entry.Validate(); err != nil {
		return Log{}, fmt.Errorf("getLog validate %s: %w: %w", id, ErrDatabase, err)
	}
	return entry, nil
}

func (r *pgxLogRepository) getLogs(ctx context.Context, userID, startDate, endDate string, limit, offset int) ([]Log, error) {
	if len(userID) > repoMaxUserIDLength {
		return nil, ErrInvalidInput
	}

	rows, err := r.pool.Query(ctx, queryGetLogs, userID, startDate, endDate, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("getLogs: %w: %w", ErrDatabase, err)
	}
	return collectLogs(rows)
}

// getLogsKeysetFirst returns the first page of a cursor walk (no cursor yet).
func (r *pgxLogRepository) getLogsKeysetFirst(ctx context.Context, userID, startDate, endDate string, limit int) ([]Log, error) {
	if len(userID) > repoMaxUserIDLength {
		return nil, ErrInvalidInput
	}
	rows, err := r.pool.Query(ctx, queryGetLogsKeysetFirst, userID, startDate, endDate, limit)
	if err != nil {
		return nil, fmt.Errorf("getLogsKeysetFirst: %w: %w", ErrDatabase, err)
	}
	return collectLogs(rows)
}

// getLogsKeysetAfter resumes a cursor walk after (cursorTime, cursorID). The
// cursor components are already validated and canonicalised by the service
// (RFC3339Nano time + uuid.Parse), so nothing unvalidated reaches SQL here.
func (r *pgxLogRepository) getLogsKeysetAfter(ctx context.Context, userID, startDate, endDate string, cursorTime time.Time, cursorID string, limit int) ([]Log, error) {
	if len(userID) > repoMaxUserIDLength {
		return nil, ErrInvalidInput
	}
	rows, err := r.pool.Query(ctx, queryGetLogsKeysetAfter, userID, startDate, endDate, cursorTime, cursorID, limit)
	if err != nil {
		return nil, fmt.Errorf("getLogsKeysetAfter: %w: %w", ErrDatabase, err)
	}
	return collectLogs(rows)
}

// collectLogs scans every row into a []Log, re-validating each (defence in
// depth, security rule 3), and returns a non-nil empty slice when there are no
// rows. Shared by the offset and keyset list queries.
func collectLogs(rows pgx.Rows) ([]Log, error) {
	defer rows.Close()

	var logs []Log
	for rows.Next() {
		var entry Log
		if err := rows.Scan(
			&entry.ID, &entry.UserID, &entry.DateAndTime, &entry.Log,
			&entry.CreatedAt, &entry.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("collectLogs scan: %w: %w", ErrDatabase, err)
		}
		if err := entry.Validate(); err != nil {
			return nil, fmt.Errorf("collectLogs validate: %w: %w", ErrDatabase, err)
		}
		logs = append(logs, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("collectLogs rows: %w: %w", ErrDatabase, err)
	}
	if len(logs) == 0 {
		return []Log{}, nil
	}
	return logs, nil
}

func (r *pgxLogRepository) createLog(ctx context.Context, userID, dateAndTime, logContent string) (Log, error) {
	if len(userID) > repoMaxUserIDLength {
		return Log{}, ErrInvalidInput
	}

	var entry Log
	err := r.pool.QueryRow(ctx, queryCreateLog, userID, dateAndTime, logContent).Scan(
		&entry.ID, &entry.UserID, &entry.DateAndTime, &entry.Log,
		&entry.CreatedAt, &entry.UpdatedAt,
	)
	if err != nil {
		return Log{}, fmt.Errorf("createLog: %w: %w", ErrDatabase, err)
	}

	if err := entry.Validate(); err != nil {
		return Log{}, fmt.Errorf("createLog validate: %w: %w", ErrDatabase, err)
	}
	return entry, nil
}

func (r *pgxLogRepository) updateLog(ctx context.Context, id, userID, logContent string) (Log, error) {
	if len(userID) > repoMaxUserIDLength {
		return Log{}, ErrInvalidInput
	}

	var entry Log
	err := r.pool.QueryRow(ctx, queryUpdateLog, logContent, id, userID).Scan(
		&entry.ID, &entry.UserID, &entry.DateAndTime, &entry.Log,
		&entry.CreatedAt, &entry.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Log{}, ErrLogNotFound
		}
		return Log{}, fmt.Errorf("updateLog %s: %w: %w", id, ErrDatabase, err)
	}

	if err := entry.Validate(); err != nil {
		return Log{}, fmt.Errorf("updateLog validate %s: %w: %w", id, ErrDatabase, err)
	}
	return entry, nil
}

func (r *pgxLogRepository) deleteLog(ctx context.Context, id, userID string) error {
	if len(userID) > repoMaxUserIDLength {
		return ErrInvalidInput
	}

	tag, err := r.pool.Exec(ctx, queryDeleteLog, id, userID)
	if err != nil {
		return fmt.Errorf("deleteLog %s: %w: %w", id, ErrDatabase, err)
	}

	if tag.RowsAffected() == 0 {
		return ErrLogNotFound
	}
	return nil
}
