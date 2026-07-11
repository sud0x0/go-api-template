package userlog

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sud0x0/go-api-template/internal/shared"
)

// stubRepo is a hand-rolled mock for the logRepository interface so the
// service can be exercised in isolation (no DB, no pgxmock, no fakes from
// the test stack).
type stubRepo struct {
	getLogFn      func(ctx context.Context, id, userID string) (Log, error)
	getLogsFn     func(ctx context.Context, userID, startDate, endDate string, limit, offset int) ([]Log, error)
	keysetFirstFn func(ctx context.Context, userID, sd, ed string, limit int) ([]Log, error)
	keysetAfterFn func(ctx context.Context, userID, sd, ed string, ct time.Time, cid string, limit int) ([]Log, error)
	createFn      func(ctx context.Context, userID, dt, content string) (Log, error)
	updateFn      func(ctx context.Context, id, userID, content string) (Log, error)
	deleteFn      func(ctx context.Context, id, userID string) error
	lastCallLog   string
}

func (s *stubRepo) getLogsKeysetFirst(ctx context.Context, userID, sd, ed string, limit int) ([]Log, error) {
	s.lastCallLog = "getLogsKeysetFirst"
	if s.keysetFirstFn != nil {
		return s.keysetFirstFn(ctx, userID, sd, ed, limit)
	}
	return nil, errors.New("keysetFirstFn not set")
}
func (s *stubRepo) getLogsKeysetAfter(ctx context.Context, userID, sd, ed string, ct time.Time, cid string, limit int) ([]Log, error) {
	s.lastCallLog = "getLogsKeysetAfter"
	if s.keysetAfterFn != nil {
		return s.keysetAfterFn(ctx, userID, sd, ed, ct, cid, limit)
	}
	return nil, errors.New("keysetAfterFn not set")
}

func (s *stubRepo) getLog(ctx context.Context, id, userID string) (Log, error) {
	s.lastCallLog = "getLog"
	if s.getLogFn != nil {
		return s.getLogFn(ctx, id, userID)
	}
	return Log{}, errors.New("getLogFn not set")
}
func (s *stubRepo) getLogs(ctx context.Context, userID, sd, ed string, l, o int) ([]Log, error) {
	s.lastCallLog = "getLogs"
	if s.getLogsFn != nil {
		return s.getLogsFn(ctx, userID, sd, ed, l, o)
	}
	return nil, errors.New("getLogsFn not set")
}
func (s *stubRepo) createLog(ctx context.Context, userID, dt, content string) (Log, error) {
	s.lastCallLog = "createLog"
	if s.createFn != nil {
		return s.createFn(ctx, userID, dt, content)
	}
	return Log{}, errors.New("createFn not set")
}
func (s *stubRepo) updateLog(ctx context.Context, id, userID, content string) (Log, error) {
	s.lastCallLog = "updateLog"
	if s.updateFn != nil {
		return s.updateFn(ctx, id, userID, content)
	}
	return Log{}, errors.New("updateFn not set")
}
func (s *stubRepo) deleteLog(ctx context.Context, id, userID string) error {
	s.lastCallLog = "deleteLog"
	if s.deleteFn != nil {
		return s.deleteFn(ctx, id, userID)
	}
	return errors.New("deleteFn not set")
}

// Cross-user (*ForTarget) stubs mirror the real repository: each delegates to
// its owner-scoped sibling with targetUserID as the scope, so a test can set a
// single fn and exercise both the self and cross-user service paths. lastCallLog
// records the *ForTarget name so a test can assert the cross-user path was taken.
func (s *stubRepo) getLogForTarget(ctx context.Context, id, targetUserID string) (Log, error) {
	l, err := s.getLog(ctx, id, targetUserID)
	s.lastCallLog = "getLogForTarget"
	return l, err
}
func (s *stubRepo) getLogsForTarget(ctx context.Context, targetUserID, sd, ed string, l, o int) ([]Log, error) {
	rows, err := s.getLogs(ctx, targetUserID, sd, ed, l, o)
	s.lastCallLog = "getLogsForTarget"
	return rows, err
}
func (s *stubRepo) getLogsKeysetFirstForTarget(ctx context.Context, targetUserID, sd, ed string, limit int) ([]Log, error) {
	rows, err := s.getLogsKeysetFirst(ctx, targetUserID, sd, ed, limit)
	s.lastCallLog = "getLogsKeysetFirstForTarget"
	return rows, err
}
func (s *stubRepo) getLogsKeysetAfterForTarget(ctx context.Context, targetUserID, sd, ed string, ct time.Time, cid string, limit int) ([]Log, error) {
	rows, err := s.getLogsKeysetAfter(ctx, targetUserID, sd, ed, ct, cid, limit)
	s.lastCallLog = "getLogsKeysetAfterForTarget"
	return rows, err
}
func (s *stubRepo) updateLogForTarget(ctx context.Context, id, targetUserID, content string) (Log, error) {
	l, err := s.updateLog(ctx, id, targetUserID, content)
	s.lastCallLog = "updateLogForTarget"
	return l, err
}
func (s *stubRepo) deleteLogForTarget(ctx context.Context, id, targetUserID string) error {
	err := s.deleteLog(ctx, id, targetUserID)
	s.lastCallLog = "deleteLogForTarget"
	return err
}

// ---- cross-user (target-scoped) service methods -----------------------------

func TestService_getLogForTarget_ScopesToTarget(t *testing.T) {
	stub := &stubRepo{
		getLogFn: func(_ context.Context, id, uid string) (Log, error) {
			if uid != "target-uuid" {
				t.Errorf("repo scope: got user %q, want the TARGET %q", uid, "target-uuid")
			}
			return Log{ID: id}, nil
		},
	}
	svc := NewLogService(stub, nil)
	if _, err := svc.getLogForTarget(context.Background(), "i", "target-uuid"); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if stub.lastCallLog != "getLogForTarget" {
		t.Errorf("cross-user path not taken: lastCall=%q", stub.lastCallLog)
	}
}

func TestService_updateLogForTarget_RejectsOverLength(t *testing.T) {
	stub := &stubRepo{}
	svc := NewLogService(stub, nil)
	overlong := strings.Repeat("a", shared.LogMaxChars+1)
	_, err := svc.updateLogForTarget(context.Background(), "i", "target-uuid", UpdateRequest{Log: overlong})
	var limitErr *shared.LimitExceededError
	if !errors.As(err, &limitErr) {
		t.Fatalf("got %v, want LimitExceededError", err)
	}
	if stub.lastCallLog == "updateLogForTarget" {
		t.Error("repo.updateLogForTarget should NOT be called for over-length input")
	}
}

func TestService_getLogsCursorForTarget_ScopesWalkToTarget(t *testing.T) {
	var seenOwner string
	stub := &stubRepo{
		keysetFirstFn: func(_ context.Context, uid, _, _ string, _ int) ([]Log, error) {
			seenOwner = uid
			return []Log{}, nil
		},
	}
	svc := NewLogService(stub, nil)
	if _, _, err := svc.getLogsCursorForTarget(context.Background(), "target-uuid", "", "", "", 10); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if seenOwner != "target-uuid" {
		t.Errorf("cursor walk owner: got %q, want the TARGET %q", seenOwner, "target-uuid")
	}
	if stub.lastCallLog != "getLogsKeysetFirstForTarget" {
		t.Errorf("cross-user cursor path not taken: lastCall=%q", stub.lastCallLog)
	}
}

// ---- getLog -----------------------------------------------------------------

func TestService_getLog_RejectsEmptyParams(t *testing.T) {
	svc := NewLogService(&stubRepo{}, nil)
	_, err := svc.getLog(context.Background(), "", "u")
	if !errors.Is(err, ErrMissingParameters) {
		t.Errorf("empty id: got %v, want ErrMissingParameters", err)
	}
	_, err = svc.getLog(context.Background(), "i", "")
	if !errors.Is(err, ErrMissingParameters) {
		t.Errorf("empty userID: got %v, want ErrMissingParameters", err)
	}
}

func TestService_getLog_DelegatesToRepo(t *testing.T) {
	stub := &stubRepo{
		getLogFn: func(_ context.Context, id, uid string) (Log, error) {
			if id != "i" || uid != "u" {
				t.Errorf("repo got (%q,%q), want (i,u)", id, uid)
			}
			return Log{ID: "i"}, nil
		},
	}
	svc := NewLogService(stub, nil)
	got, err := svc.getLog(context.Background(), "i", "u")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "i" {
		t.Errorf("got %+v", got)
	}
}

// ---- getLogs ----------------------------------------------------------------

func TestService_getLogs_AppliesDateDefaultsAndPassesPagination(t *testing.T) {
	var captured struct {
		startDate, endDate string
		limit, offset      int
	}
	stub := &stubRepo{
		getLogsFn: func(_ context.Context, _, sd, ed string, l, o int) ([]Log, error) {
			captured.startDate, captured.endDate = sd, ed
			captured.limit, captured.offset = l, o
			return []Log{}, nil
		},
	}
	svc := NewLogService(stub, nil)

	// empty dates → wide range. limit/offset are passed through verbatim: the
	// handler's shared.ValidatePagination already normalised them (it never
	// returns limit<1 or offset<0), so the service applies no fallback.
	if _, err := svc.getLogs(context.Background(), "u", "", "", shared.DefaultPageSize, 0); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if captured.limit != shared.DefaultPageSize {
		t.Errorf("limit pass-through: got %d want %d", captured.limit, shared.DefaultPageSize)
	}
	if captured.offset != 0 {
		t.Errorf("offset pass-through: got %d want 0", captured.offset)
	}
	if !strings.HasPrefix(captured.startDate, "1970-") {
		t.Errorf("startDate default: got %q", captured.startDate)
	}
	if !strings.HasPrefix(captured.endDate, "2099-") {
		t.Errorf("endDate default: got %q", captured.endDate)
	}
}

func TestService_getLogs_RejectsMalformedDate(t *testing.T) {
	svc := NewLogService(&stubRepo{}, nil)
	_, err := svc.getLogs(context.Background(), "u", "yesterday", "", 10, 0)
	if !errors.Is(err, ErrInvalidDateTime) {
		t.Errorf("got %v, want ErrInvalidDateTime", err)
	}
}

// TestService_getLogs_RejectsInvertedRange verifies start_date > end_date is a
// bounded 400-mapped error (ErrInvalidDateRange), not a silently-empty result.
// The repo is never called because normaliseDateRange rejects before any query.
func TestService_getLogs_RejectsInvertedRange(t *testing.T) {
	stub := &stubRepo{} // no getLogsFn set: a call would return the "not set" error
	svc := NewLogService(stub, nil)
	_, err := svc.getLogs(context.Background(), "u",
		"2025-12-31T00:00:00Z", "2025-01-01T00:00:00Z", 10, 0)
	if !errors.Is(err, ErrInvalidDateRange) {
		t.Fatalf("inverted range: got %v, want ErrInvalidDateRange", err)
	}
	if stub.lastCallLog == "getLogs" {
		t.Error("repo.getLogs must not be called for an inverted range")
	}

	// Equal bounds are a valid (single-instant) range and must reach the repo.
	stub2 := &stubRepo{getLogsFn: func(_ context.Context, _, _, _ string, _, _ int) ([]Log, error) {
		return []Log{}, nil
	}}
	svc2 := NewLogService(stub2, nil)
	if _, err := svc2.getLogs(context.Background(), "u",
		"2025-06-01T00:00:00Z", "2025-06-01T00:00:00Z", 10, 0); err != nil {
		t.Errorf("equal bounds should be a valid range, got %v", err)
	}
}

// ---- createLog --------------------------------------------------------------

func TestService_createLog_RejectsOverLength(t *testing.T) {
	stub := &stubRepo{}
	svc := NewLogService(stub, nil)
	overlong := strings.Repeat("a", shared.LogMaxChars+1)
	_, err := svc.createLog(context.Background(), "u", CreateRequest{
		DateAndTime: "2025-01-01T00:00:00Z",
		Log:         overlong,
	})
	var limitErr *shared.LimitExceededError
	if !errors.As(err, &limitErr) {
		t.Fatalf("got %v, want LimitExceededError", err)
	}
	if limitErr.Limit != shared.LogMaxChars || limitErr.Current != shared.LogMaxChars+1 {
		t.Errorf("limit fields: %+v", limitErr)
	}
	if stub.lastCallLog == "createLog" {
		t.Error("repo.createLog should NOT have been called for over-length input")
	}
}

func TestService_createLog_RejectsBadDate(t *testing.T) {
	svc := NewLogService(&stubRepo{}, nil)
	_, err := svc.createLog(context.Background(), "u", CreateRequest{
		DateAndTime: "yesterday",
		Log:         "x",
	})
	if !errors.Is(err, ErrInvalidDateTime) {
		t.Errorf("got %v, want ErrInvalidDateTime", err)
	}
}

// ---- updateLog --------------------------------------------------------------

func TestService_updateLog_RejectsOverLength(t *testing.T) {
	svc := NewLogService(&stubRepo{}, nil)
	overlong := strings.Repeat("a", shared.LogMaxChars+1)
	_, err := svc.updateLog(context.Background(), "i", "u", UpdateRequest{Log: overlong})
	var limitErr *shared.LimitExceededError
	if !errors.As(err, &limitErr) {
		t.Errorf("got %v, want LimitExceededError", err)
	}
}

// ---- deleteLog --------------------------------------------------------------

func TestService_deleteLog_DelegatesToRepo(t *testing.T) {
	called := false
	stub := &stubRepo{
		deleteFn: func(_ context.Context, _, _ string) error {
			called = true
			return nil
		},
	}
	svc := NewLogService(stub, nil)
	if err := svc.deleteLog(context.Background(), "i", "u"); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !called {
		t.Error("repo.deleteLog was not called")
	}
}
