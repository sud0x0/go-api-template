package userlog

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sud0x0/go-api-template/internal/shared"
)

// logService defines the interface for log business logic.
//
// As in the repository, the plain methods are the SELF-ACCESS path and the
// *ForTarget methods are the CROSS-USER path (caller acts on targetUserID's
// rows, only after an OPA allow). They apply the identical business validation
// and differ only in which owner the row-ownership query is scoped to. Creation
// has no cross-user variant: a log is always created under its caller's own id.
type logService interface {
	getLog(ctx context.Context, id, userID string) (Log, error)
	getLogs(ctx context.Context, userID, startDate, endDate string, limit, offset int) ([]Log, error)
	getLogsCursor(ctx context.Context, userID, startDate, endDate, cursor string, limit int) ([]Log, string, error)
	createLog(ctx context.Context, userID string, req CreateRequest) (Log, error)
	createLogs(ctx context.Context, userID string, reqs []CreateRequest) ([]Log, error)
	updateLog(ctx context.Context, id, userID string, req UpdateRequest) (Log, error)
	deleteLog(ctx context.Context, id, userID string) error

	getLogForTarget(ctx context.Context, id, targetUserID string) (Log, error)
	getLogsForTarget(ctx context.Context, targetUserID, startDate, endDate string, limit, offset int) ([]Log, error)
	getLogsCursorForTarget(ctx context.Context, targetUserID, startDate, endDate, cursor string, limit int) ([]Log, string, error)
	updateLogForTarget(ctx context.Context, id, targetUserID string, req UpdateRequest) (Log, error)
	deleteLogForTarget(ctx context.Context, id, targetUserID string) error
}

// txRunner is the transaction-control capability the service needs for
// multi-statement atomic operations. *db.DB satisfies it (DB.WithTransaction).
// It is an interface (not a concrete *db.DB) so the service stays unit-testable
// with a pgxmock-backed fake: transaction control lives in the SERVICE layer,
// repositories stay transaction-agnostic.
type txRunner interface {
	WithTransaction(ctx context.Context, fn func(tx pgx.Tx) error) error
}

// defaultLogService implements logService.
type defaultLogService struct {
	repo logRepository // single-row paths (already atomic, one statement each)
	tx   txRunner      // multi-statement atomic paths (createLogs)
}

// NewLogService creates a new logService.
//
// It takes both the repository (for single-row paths, each of which is a single
// already-atomic statement) and a txRunner (*db.DB) for multi-statement atomic
// operations like createLogs. The service does not log; it returns wrapped
// sentinel errors and lets the handler log/count them, so no logger is taken.
func NewLogService(repo logRepository, tx txRunner) *defaultLogService {
	return &defaultLogService{repo: repo, tx: tx}
}

func (s *defaultLogService) getLog(ctx context.Context, id, userID string) (Log, error) {
	if id == "" || userID == "" {
		return Log{}, ErrMissingParameters
	}
	return s.repo.getLog(ctx, id, userID)
}

// normaliseDateRange applies the wide-range defaults to an omitted start/end
// date and validates both bounds as RFC3339, returning a non-batch
// InvalidDateTimeError (Index = -1) naming the offending bound (Field
// "start_date" or "end_date") on failure, so the operator and client can tell
// WHICH bound was malformed. Shared by the offset and cursor list paths so the
// defaults and validation live in one place. The defaults are documented to
// callers via the OpenAPI spec.
func normaliseDateRange(startDate, endDate string) (string, string, error) {
	if startDate == "" {
		startDate = "1970-01-01T00:00:00Z"
	}
	if endDate == "" {
		endDate = "2099-12-31T23:59:59Z"
	}
	start, err := time.Parse(time.RFC3339, startDate)
	if err != nil {
		return "", "", &InvalidDateTimeError{Field: "start_date", Index: -1}
	}
	end, err := time.Parse(time.RFC3339, endDate)
	if err != nil {
		return "", "", &InvalidDateTimeError{Field: "end_date", Index: -1}
	}
	// An inverted range (start after end) matches no rows in the BETWEEN query, so
	// without this check a client typo silently returns an empty list. Reject it
	// as a 400 instead (ErrInvalidDateRange). Equal bounds are allowed (a valid,
	// possibly-single-instant range).
	if start.After(end) {
		return "", "", ErrInvalidDateRange
	}
	return startDate, endDate, nil
}

func (s *defaultLogService) getLogs(ctx context.Context, userID, startDate, endDate string, limit, offset int) ([]Log, error) {
	if userID == "" {
		return nil, ErrMissingParameters
	}

	// limit and offset are already validated and normalised by
	// shared.ValidatePagination in the handler: limit is guaranteed >= 1 and
	// offset >= 0, so no fallback is applied (or needed) here. The single
	// source of truth for the default page size is shared.DefaultPageSize.

	startDate, endDate, err := normaliseDateRange(startDate, endDate)
	if err != nil {
		return nil, err
	}

	return s.repo.getLogs(ctx, userID, startDate, endDate, limit, offset)
}

// getLogsCursor is the keyset (cursor) list path. It returns a page of logs and
// the next_cursor to fetch the following page (empty string on the last page).
//
// An empty cursor means the first page. A non-empty cursor is decoded and
// strictly validated (decodeCursor) before any SQL. A malformed cursor returns
// shared.ErrInvalidPagination (400), never reaching the query.
//
// It fetches one extra row (limit+1) so it can tell whether a further page
// exists without a second round-trip: if the repo returns more than `limit`
// rows, there is more, and the next_cursor is the keyset of the limit-th row.
func (s *defaultLogService) getLogsCursor(ctx context.Context, userID, startDate, endDate, cursor string, limit int) ([]Log, string, error) {
	return s.getLogsCursorScoped(ctx, userID, startDate, endDate, cursor, limit,
		s.repo.getLogsKeysetFirst, s.repo.getLogsKeysetAfter)
}

// keysetFirstFn / keysetAfterFn are the two repository entry points a cursor
// walk needs. They are threaded into getLogsCursorScoped so the SELF and
// CROSS-USER cursor paths share ONE copy of the pagination logic (fetch+1,
// next-cursor computation) and differ only in which owner-scoped repository
// methods they call. This single-sourcing is the guarantee behind
// decisions.md's "cursor stays within one owner": self and cross-user cannot
// drift because they run the same code.
type keysetFirstFn func(ctx context.Context, ownerID, startDate, endDate string, limit int) ([]Log, error)
type keysetAfterFn func(ctx context.Context, ownerID, startDate, endDate string, cursorTime time.Time, cursorID string, limit int) ([]Log, error)

// getLogsCursorScoped is the keyset (cursor) list path, parameterised by the
// owner-scoped repository methods it walks. See getLogsCursor's original doc:
// an empty cursor is the first page; a non-empty cursor is strictly decoded
// (decodeCursor) before any SQL; it fetches limit+1 to detect a further page
// without a second round-trip. The whole walk is scoped to a SINGLE ownerID
// (caller for self-access, target for cross-user), so a page never mixes owners
// and the next_cursor only ever repositions within that one owner's rows.
func (s *defaultLogService) getLogsCursorScoped(ctx context.Context, ownerID, startDate, endDate, cursor string, limit int, first keysetFirstFn, after keysetAfterFn) ([]Log, string, error) {
	if ownerID == "" {
		return nil, "", ErrMissingParameters
	}

	startDate, endDate, err := normaliseDateRange(startDate, endDate)
	if err != nil {
		return nil, "", err
	}

	fetch := limit + 1 // one extra row to detect a further page

	var rows []Log
	if cursor == "" {
		rows, err = first(ctx, ownerID, startDate, endDate, fetch)
	} else {
		cursorTime, cursorID, derr := decodeCursor(cursor)
		if derr != nil {
			return nil, "", derr
		}
		rows, err = after(ctx, ownerID, startDate, endDate, cursorTime, cursorID, fetch)
	}
	if err != nil {
		return nil, "", err
	}

	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = encodeCursor(last.DateAndTime, last.ID)
	}
	return rows, next, nil
}

func (s *defaultLogService) createLog(ctx context.Context, userID string, req CreateRequest) (Log, error) {
	if userID == "" {
		return Log{}, ErrMissingParameters
	}

	runeCount := shared.RuneCountLen(req.Log)
	if runeCount > shared.LogMaxChars {
		return Log{}, NewLogTooLongError(runeCount)
	}

	if _, err := time.Parse(time.RFC3339, req.DateAndTime); err != nil {
		return Log{}, &InvalidDateTimeError{Field: "date_and_time", Index: -1}
	}

	return s.repo.createLog(ctx, userID, req.DateAndTime, req.Log)
}

// createLogs creates every entry atomically (any failure rolls back the lot)
// and returns the created entries in request order.
//
// THIS IS THE CANONICAL MULTI-STATEMENT TRANSACTION PATTERN FOR THE TEMPLATE.
// Transaction control sits in the service layer: the service runs
// db.WithTransaction and, inside the closure, constructs a repository over the
// pgx.Tx. Repositories accept db.PgxIface, which pgx.Tx satisfies, so a
// tx-scoped repository is just NewLogRepository(tx): the same repository code
// runs inside and outside a transaction. Single-row methods above deliberately
// do NOT use a transaction: a single statement is already atomic, so wrapping it
// would add a round-trip for nothing. Reach for WithTransaction only when two or
// more statements must commit or roll back together.
func (s *defaultLogService) createLogs(ctx context.Context, userID string, reqs []CreateRequest) ([]Log, error) {
	if userID == "" {
		return nil, ErrMissingParameters
	}

	// Validate every entry up front (no DB work) so a malformed entry fails
	// before a transaction (and its row locks) is ever opened. Same per-entry
	// rules as a single create (rune limit + RFC3339 date). The error TYPE stays
	// bounded; the failing index now reaches the client for BOTH failure kinds:
	// over-length via NewLogTooLongErrorAt and bad-date via InvalidDateTimeError
	// (Field "date_and_time"), so the message reads "entry N: date_and_time: ...".
	for i := range reqs {
		runeCount := shared.RuneCountLen(reqs[i].Log)
		if runeCount > shared.LogMaxChars {
			return nil, NewLogTooLongErrorAt(i, runeCount)
		}
		if _, err := time.Parse(time.RFC3339, reqs[i].DateAndTime); err != nil {
			return nil, &InvalidDateTimeError{Field: "date_and_time", Index: i}
		}
	}

	var created []Log
	err := s.tx.WithTransaction(ctx, func(tx pgx.Tx) error {
		txRepo := NewLogRepository(tx)
		out := make([]Log, 0, len(reqs))
		for i := range reqs {
			entry, err := txRepo.createLog(ctx, userID, reqs[i].DateAndTime, reqs[i].Log)
			if err != nil {
				// Returning any error rolls back every insert in this batch.
				return err
			}
			out = append(out, entry)
		}
		created = out
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (s *defaultLogService) updateLog(ctx context.Context, id, userID string, req UpdateRequest) (Log, error) {
	if id == "" || userID == "" {
		return Log{}, ErrMissingParameters
	}

	runeCount := shared.RuneCountLen(req.Log)
	if runeCount > shared.LogMaxChars {
		return Log{}, NewLogTooLongError(runeCount)
	}

	return s.repo.updateLog(ctx, id, userID, req.Log)
}

func (s *defaultLogService) deleteLog(ctx context.Context, id, userID string) error {
	if id == "" || userID == "" {
		return ErrMissingParameters
	}
	return s.repo.deleteLog(ctx, id, userID)
}

// --- Cross-user (target-scoped) methods ---
//
// Each applies the SAME business validation as its self-access sibling and then
// calls the repository's target-scoped method. They are only reached after the
// OPA middleware authorised the caller to act on targetUserID; the service does
// not re-check that. Row ownership is still enforced in SQL (the *ForTarget
// repo methods scope by targetUserID), so a not-owned object is a clean
// not-found, never a cross-owner leak.

func (s *defaultLogService) getLogForTarget(ctx context.Context, id, targetUserID string) (Log, error) {
	if id == "" || targetUserID == "" {
		return Log{}, ErrMissingParameters
	}
	return s.repo.getLogForTarget(ctx, id, targetUserID)
}

func (s *defaultLogService) getLogsForTarget(ctx context.Context, targetUserID, startDate, endDate string, limit, offset int) ([]Log, error) {
	if targetUserID == "" {
		return nil, ErrMissingParameters
	}
	startDate, endDate, err := normaliseDateRange(startDate, endDate)
	if err != nil {
		return nil, err
	}
	return s.repo.getLogsForTarget(ctx, targetUserID, startDate, endDate, limit, offset)
}

func (s *defaultLogService) getLogsCursorForTarget(ctx context.Context, targetUserID, startDate, endDate, cursor string, limit int) ([]Log, string, error) {
	return s.getLogsCursorScoped(ctx, targetUserID, startDate, endDate, cursor, limit,
		s.repo.getLogsKeysetFirstForTarget, s.repo.getLogsKeysetAfterForTarget)
}

func (s *defaultLogService) updateLogForTarget(ctx context.Context, id, targetUserID string, req UpdateRequest) (Log, error) {
	if id == "" || targetUserID == "" {
		return Log{}, ErrMissingParameters
	}
	runeCount := shared.RuneCountLen(req.Log)
	if runeCount > shared.LogMaxChars {
		return Log{}, NewLogTooLongError(runeCount)
	}
	return s.repo.updateLogForTarget(ctx, id, targetUserID, req.Log)
}

func (s *defaultLogService) deleteLogForTarget(ctx context.Context, id, targetUserID string) error {
	if id == "" || targetUserID == "" {
		return ErrMissingParameters
	}
	return s.repo.deleteLogForTarget(ctx, id, targetUserID)
}
