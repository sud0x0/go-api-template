package userlog

// These are UNIT tests using pgxmock. They exercise the handler → service →
// repository chain end-to-end inside the process, but with a mock database.
// They do not catch SQL syntax errors, schema mismatches, or driver-level
// encoding bugs. For those see log_integration_test.go (build tag
// `integration`) which runs against a real Postgres.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/sud0x0/go-api-template/internal/shared"
	"github.com/sud0x0/go-api-template/internal/shared/logger"
)

// ============================================================================
// TEST HELPERS
// ============================================================================

type mockLogger struct{}

func (m *mockLogger) LogError(simplifiedError, actualError error)              {}
func (m *mockLogger) LogWarn(message string, args ...any)                      {}
func (m *mockLogger) LogInfo(message string, args ...any)                      {}
func (m *mockLogger) LogDebug(message string, args ...any)                     {}
func (m *mockLogger) WithRequestContext(_ logger.RequestContext) logger.Logger { return m }

// testHandlerMetrics implements HandlerMetrics with an in-memory count keyed by
// (feature, error_type) so tests can read back recorded errors without any
// telemetry backend. Concurrency-safe so it survives -race.
type testHandlerMetrics struct {
	mu     sync.Mutex
	counts map[[2]string]float64
}

func (m *testHandlerMetrics) IncAPIError(feature, errorType string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counts[[2]string{feature, errorType}]++
}

func (m *testHandlerMetrics) count(feature, errorType string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counts[[2]string{feature, errorType}]
}

func newTestHandlerMetrics() *testHandlerMetrics {
	return &testHandlerMetrics{counts: map[[2]string]float64{}}
}

// mockTxRunner adapts a pgxmock pool to the service's txRunner interface,
// mirroring db.DB.WithTransaction: it Begins on the mock, runs fn with the
// resulting pgx.Tx (which the repository can be built over), and commits or
// rolls back. Tests drive it with mock.ExpectBegin / ExpectQuery / ExpectCommit
// / ExpectRollback, the same pattern internal/db's transaction tests use.
type mockTxRunner struct{ pool pgxmock.PgxPoolIface }

func (m *mockTxRunner) WithTransaction(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

func setupTestStack(t *testing.T) (*Handler, pgxmock.PgxPoolIface, *testHandlerMetrics, func()) {
	t.Helper()
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("Failed to create pgxmock pool: %v", err)
	}

	log := &mockLogger{}
	m := newTestHandlerMetrics()
	repo := NewLogRepository(mock)
	service := NewLogService(repo, &mockTxRunner{pool: mock})
	handler := NewLogHandler(service, log, m)

	return handler, mock, m, func() { mock.Close() }
}

// assertErrorRecorded verifies the api.errors count for
// {feature="log",error_type=errType} equals want. Used by every error-path
// subtest to enforce the spec rule that errors must be counted with the right
// bounded label.
func assertErrorRecorded(t *testing.T, m *testHandlerMetrics, errType string, want float64) {
	t.Helper()
	got := m.count(FeatureName, errType)
	if got != want {
		t.Errorf("api.errors{feature=%q,error_type=%q}: got %v want %v",
			FeatureName, errType, got, want)
	}
}

const testUserID = "550e8400-e29b-41d4-a716-446655440000"
const testLogID = "660e8400-e29b-41d4-a716-446655440001"
const missingLogID = "770e8400-e29b-41d4-a716-446655440002"

func createRequest(method, path string, body []byte, urlParams map[string]string, queryParams map[string]string) *http.Request {
	req := httptest.NewRequest(method, path, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	ctx := shared.WithUserID(req.Context(), testUserID)

	if len(queryParams) > 0 {
		q := req.URL.Query()
		for key, value := range queryParams {
			q.Add(key, value)
		}
		req.URL.RawQuery = q.Encode()
	}

	if len(urlParams) > 0 {
		chiCtx := chi.NewRouteContext()
		for key, value := range urlParams {
			chiCtx.URLParams.Add(key, value)
		}
		ctx = context.WithValue(ctx, chi.RouteCtxKey, chiCtx)
	}

	return req.WithContext(ctx)
}

// createRequestNoUser is like createRequest but doesn't set the user ID in
// context, used to trigger the unauthorised path.
func createRequestNoUser(method, path string, body []byte, urlParams map[string]string) *http.Request {
	req := httptest.NewRequest(method, path, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	if len(urlParams) > 0 {
		chiCtx := chi.NewRouteContext()
		for key, value := range urlParams {
			chiCtx.URLParams.Add(key, value)
		}
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, chiCtx))
	}
	return req
}

func mockLogRows(mock pgxmock.PgxPoolIface) *pgxmock.Rows {
	return mock.NewRows([]string{
		"id", "user_id", "date_and_time", "log", "created_at", "updated_at",
	})
}

// testTargetUserID is a SECOND user, distinct from testUserID (the caller). It
// stands in for the owner of a cross-user request.
const testTargetUserID = "990e8400-e29b-41d4-a716-446655440009"

// withCrossUserTarget stamps the canonical target owner on a request's context
// exactly as the OPA authorisation middleware does AFTER an allow. Handlers read
// it back via shared.TargetUserFromContext and take the cross-user (target-aware)
// path. Setting it directly here mirrors "OPA already authorised this tuple".
func withCrossUserTarget(req *http.Request, target string) *http.Request {
	return req.WithContext(shared.WithTargetUser(req.Context(), target))
}

// ============================================================================
// TESTS
// ============================================================================

// TestLogHandler_CrossUser proves the cross-user path: when the OPA middleware
// has authorised a target (set on the context), the handler scopes reads and
// writes to the TARGET's user_id, never the caller's. Every mock expectation
// asserts WithArgs(testTargetUserID, …): if the handler passed the caller
// (testUserID) instead, ExpectationsWereMet would fail. This is the unit-level
// counterpart to the integration cross-user test.
func TestLogHandler_CrossUser(t *testing.T) {
	testDateTime := "2025-12-28T10:30:00Z"
	now := time.Now()
	parsedTime, _ := time.Parse(time.RFC3339, testDateTime)

	t.Run("GET scopes to the target owner", func(t *testing.T) {
		handler, mock, _, cleanup := setupTestStack(t)
		defer cleanup()

		mock.ExpectQuery(`SELECT .+ FROM logs WHERE id = \$1 AND user_id = \$2`).
			WithArgs(testLogID, testTargetUserID). // target, not caller
			WillReturnRows(mockLogRows(mock).AddRow(
				testLogID, testTargetUserID, parsedTime, "target's row", now, now,
			))

		req := createRequest(http.MethodGet, "/api/v1/logs/"+testLogID, nil,
			map[string]string{"id": testLogID}, nil)
		req = withCrossUserTarget(req, testTargetUserID)
		rr := httptest.NewRecorder()
		handler.GetLog(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("got %d want 200. Body: %s", rr.Code, rr.Body.String())
		}
		var resp Log
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.UserID != testTargetUserID {
			t.Errorf("returned row owner: got %q, want the target %q", resp.UserID, testTargetUserID)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unfulfilled (caller leaked instead of target?): %s", err)
		}
	})

	t.Run("UPDATE scopes to the target owner", func(t *testing.T) {
		handler, mock, _, cleanup := setupTestStack(t)
		defer cleanup()

		mock.ExpectQuery(`UPDATE logs SET log = \$1 WHERE id = \$2 AND user_id = \$3 RETURNING .+`).
			WithArgs("edited by admin", testLogID, testTargetUserID).
			WillReturnRows(mockLogRows(mock).AddRow(
				testLogID, testTargetUserID, parsedTime, "edited by admin", now, now,
			))

		body, _ := json.Marshal(UpdateRequest{Log: "edited by admin"})
		req := createRequest(http.MethodPut, "/api/v1/logs/"+testLogID, body,
			map[string]string{"id": testLogID}, nil)
		req = withCrossUserTarget(req, testTargetUserID)
		rr := httptest.NewRecorder()
		handler.UpdateLog(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("got %d want 200. Body: %s", rr.Code, rr.Body.String())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unfulfilled: %s", err)
		}
	})

	t.Run("DELETE scopes to the target owner", func(t *testing.T) {
		handler, mock, _, cleanup := setupTestStack(t)
		defer cleanup()

		mock.ExpectExec(`DELETE FROM logs WHERE id = \$1 AND user_id = \$2`).
			WithArgs(testLogID, testTargetUserID).
			WillReturnResult(pgxmock.NewResult("DELETE", 1))

		req := createRequest(http.MethodDelete, "/api/v1/logs/"+testLogID, nil,
			map[string]string{"id": testLogID}, nil)
		req = withCrossUserTarget(req, testTargetUserID)
		rr := httptest.NewRecorder()
		handler.DeleteLog(rr, req)

		if rr.Code != http.StatusNoContent {
			t.Fatalf("got %d want 204. Body: %s", rr.Code, rr.Body.String())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unfulfilled: %s", err)
		}
	})

	// The highest-exposure path: a cross-user OFFSET list must return ONLY the
	// target's rows (the query is scoped to the target's user_id).
	t.Run("offset LIST returns only the target's rows", func(t *testing.T) {
		handler, mock, _, cleanup := setupTestStack(t)
		defer cleanup()

		mock.ExpectQuery(`SELECT .+ FROM logs WHERE user_id = \$1 AND date_and_time >= \$2 AND date_and_time <= \$3 ORDER BY date_and_time DESC, id DESC LIMIT \$4 OFFSET \$5`).
			WithArgs(testTargetUserID, "2025-12-01T00:00:00Z", "2025-12-31T23:59:59Z", shared.DefaultPageSize, 0).
			WillReturnRows(mockLogRows(mock).
				AddRow(testLogID, testTargetUserID, parsedTime, "t1", now, now).
				AddRow("880e8400-e29b-41d4-a716-446655440003", testTargetUserID, parsedTime, "t2", now, now))

		req := createRequest(http.MethodGet, "/api/v1/logs", nil, nil, map[string]string{
			"start_date": "2025-12-01T00:00:00Z",
			"end_date":   "2025-12-31T23:59:59Z",
		})
		req = withCrossUserTarget(req, testTargetUserID)
		rr := httptest.NewRecorder()
		handler.ListLogs(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("got %d want 200. Body: %s", rr.Code, rr.Body.String())
		}
		var resp []Log
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(resp) != 2 {
			t.Fatalf("got %d rows want 2", len(resp))
		}
		for _, l := range resp {
			if l.UserID != testTargetUserID {
				t.Errorf("cross-user list leaked a non-target row: owner %q", l.UserID)
			}
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unfulfilled (caller rows leaked?): %s", err)
		}
	})

	// A cross-user CURSOR walk stays scoped to the target: the keyset query is
	// scoped to the target's user_id and next_cursor only repositions within the
	// target's rows.
	t.Run("cursor LIST stays within the target's rows", func(t *testing.T) {
		handler, mock, _, cleanup := setupTestStack(t)
		defer cleanup()

		// limit defaults to DefaultPageSize; the service fetches limit+1 to detect a
		// further page. Two rows (< limit) → last page → no next_cursor.
		mock.ExpectQuery(`SELECT .+ FROM logs WHERE user_id = \$1 AND date_and_time >= \$2 AND date_and_time <= \$3 ORDER BY date_and_time DESC, id DESC LIMIT \$4`).
			WithArgs(testTargetUserID, "1970-01-01T00:00:00Z", "2099-12-31T23:59:59Z", shared.DefaultPageSize+1).
			WillReturnRows(mockLogRows(mock).
				AddRow(testLogID, testTargetUserID, parsedTime, "t1", now, now).
				AddRow("880e8400-e29b-41d4-a716-446655440003", testTargetUserID, parsedTime, "t2", now, now))

		req := createRequest(http.MethodGet, "/api/v1/logs", nil, nil, map[string]string{"cursor": ""})
		req = withCrossUserTarget(req, testTargetUserID)
		rr := httptest.NewRecorder()
		handler.ListLogs(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("got %d want 200. Body: %s", rr.Code, rr.Body.String())
		}
		var page CursorPage
		if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		for _, l := range page.Logs {
			if l.UserID != testTargetUserID {
				t.Errorf("cross-user cursor leaked a non-target row: owner %q", l.UserID)
			}
		}
		if page.NextCursor != "" {
			t.Errorf("last page must omit next_cursor, got %q", page.NextCursor)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unfulfilled: %s", err)
		}
	})

	// Self-access is unchanged: with no target on the context the handler scopes
	// to the CALLER, calling the owner method (not a *ForTarget method).
	t.Run("self-access still scopes to the caller", func(t *testing.T) {
		handler, mock, _, cleanup := setupTestStack(t)
		defer cleanup()

		mock.ExpectQuery(`SELECT .+ FROM logs WHERE id = \$1 AND user_id = \$2`).
			WithArgs(testLogID, testUserID). // caller, no target set
			WillReturnRows(mockLogRows(mock).AddRow(
				testLogID, testUserID, parsedTime, "own row", now, now,
			))

		req := createRequest(http.MethodGet, "/api/v1/logs/"+testLogID, nil,
			map[string]string{"id": testLogID}, nil)
		rr := httptest.NewRecorder()
		handler.GetLog(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("got %d want 200. Body: %s", rr.Code, rr.Body.String())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unfulfilled: %s", err)
		}
	})

	// A target EQUAL to the caller is self-access, not cross-user: it must scope to
	// the caller (the effectiveOwner crossUser flag is false when target == caller).
	t.Run("target equal to caller is self-access", func(t *testing.T) {
		handler, mock, _, cleanup := setupTestStack(t)
		defer cleanup()

		mock.ExpectQuery(`SELECT .+ FROM logs WHERE id = \$1 AND user_id = \$2`).
			WithArgs(testLogID, testUserID).
			WillReturnRows(mockLogRows(mock).AddRow(
				testLogID, testUserID, parsedTime, "own row", now, now,
			))

		req := createRequest(http.MethodGet, "/api/v1/logs/"+testLogID, nil,
			map[string]string{"id": testLogID}, nil)
		req = withCrossUserTarget(req, testUserID) // target == caller
		rr := httptest.NewRecorder()
		handler.GetLog(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("got %d want 200. Body: %s", rr.Code, rr.Body.String())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unfulfilled: %s", err)
		}
	})
}

func TestLogHandler(t *testing.T) {
	testDateTime := "2025-12-28T10:30:00Z"
	now := time.Now()

	// NOTE: TEST 1: CREATE A LOG
	t.Run("Create a Log", func(t *testing.T) {
		handler, mock, _, cleanup := setupTestStack(t)
		defer cleanup()

		parsedTime, _ := time.Parse(time.RFC3339, testDateTime)

		mock.ExpectQuery(`INSERT INTO logs .+ RETURNING .+`).
			WithArgs(testUserID, testDateTime, "Test Log Entry").
			WillReturnRows(mockLogRows(mock).AddRow(
				testLogID, testUserID, parsedTime, "Test Log Entry", now, now,
			))

		logReq := CreateRequest{DateAndTime: testDateTime, Log: "Test Log Entry"}
		reqBody, _ := json.Marshal(logReq)

		req := createRequest(http.MethodPost, "/api/v1/logs", reqBody, nil, nil)
		rr := httptest.NewRecorder()

		handler.CreateLog(rr, req)

		if status := rr.Code; status != http.StatusCreated {
			t.Errorf("got %v want %v. Body: %s", status, http.StatusCreated, rr.Body.String())
		}

		var response Log
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}
		if response.Log != "Test Log Entry" {
			t.Errorf("got %s, want 'Test Log Entry'", response.Log)
		}

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %s", err)
		}
	})

	// NOTE: TEST 2: GET A LOG BY ID
	t.Run("Get Log By ID", func(t *testing.T) {
		handler, mock, _, cleanup := setupTestStack(t)
		defer cleanup()

		parsedTime, _ := time.Parse(time.RFC3339, testDateTime)

		mock.ExpectQuery(`SELECT .+ FROM logs WHERE id = \$1 AND user_id = \$2`).
			WithArgs(testLogID, testUserID).
			WillReturnRows(mockLogRows(mock).AddRow(
				testLogID, testUserID, parsedTime, "Test Log Entry", now, now,
			))

		urlParams := map[string]string{"id": testLogID}
		req := createRequest(http.MethodGet, fmt.Sprintf("/api/v1/logs/%s", testLogID), nil, urlParams, nil)
		rr := httptest.NewRecorder()

		handler.GetLog(rr, req)

		if status := rr.Code; status != http.StatusOK {
			t.Errorf("got %v want %v. Body: %s", status, http.StatusOK, rr.Body.String())
		}

		var response Log
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}
		if response.ID != testLogID {
			t.Errorf("got %s want %s", response.ID, testLogID)
		}

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %s", err)
		}
	})

	// NOTE: TEST 3: UPDATE LOG
	t.Run("Update Log", func(t *testing.T) {
		handler, mock, _, cleanup := setupTestStack(t)
		defer cleanup()

		parsedTime, _ := time.Parse(time.RFC3339, testDateTime)

		mock.ExpectQuery(`UPDATE logs SET log = \$1 WHERE id = \$2 AND user_id = \$3 RETURNING .+`).
			WithArgs("Updated Log Entry", testLogID, testUserID).
			WillReturnRows(mockLogRows(mock).AddRow(
				testLogID, testUserID, parsedTime, "Updated Log Entry", now, now,
			))

		logReq := UpdateRequest{Log: "Updated Log Entry"}
		reqBody, _ := json.Marshal(logReq)

		urlParams := map[string]string{"id": testLogID}
		req := createRequest(http.MethodPut, fmt.Sprintf("/api/v1/logs/%s", testLogID), reqBody, urlParams, nil)
		rr := httptest.NewRecorder()

		handler.UpdateLog(rr, req)

		if status := rr.Code; status != http.StatusOK {
			t.Errorf("got %v want %v. Body: %s", status, http.StatusOK, rr.Body.String())
		}

		var response Log
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}
		if response.Log != "Updated Log Entry" {
			t.Errorf("got %s, want 'Updated Log Entry'", response.Log)
		}

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %s", err)
		}
	})

	// NOTE: TEST 4: LIST LOGS
	t.Run("List Logs", func(t *testing.T) {
		handler, mock, _, cleanup := setupTestStack(t)
		defer cleanup()

		parsedTime, _ := time.Parse(time.RFC3339, testDateTime)
		otherID := "880e8400-e29b-41d4-a716-446655440003"

		mock.ExpectQuery(`SELECT .+ FROM logs WHERE user_id = \$1 AND date_and_time >= \$2 AND date_and_time <= \$3 ORDER BY date_and_time DESC, id DESC LIMIT \$4 OFFSET \$5`).
			WithArgs(testUserID, "2025-12-01T00:00:00Z", "2025-12-31T23:59:59Z", shared.DefaultPageSize, 0).
			WillReturnRows(mockLogRows(mock).
				AddRow(testLogID, testUserID, parsedTime, "Log 1", now, now).
				AddRow(otherID, testUserID, parsedTime, "Log 2", now, now))

		queryParams := map[string]string{
			"start_date": "2025-12-01T00:00:00Z",
			"end_date":   "2025-12-31T23:59:59Z",
		}

		req := createRequest(http.MethodGet, "/api/v1/logs", nil, nil, queryParams)
		rr := httptest.NewRecorder()

		handler.ListLogs(rr, req)

		if status := rr.Code; status != http.StatusOK {
			t.Errorf("got %v want %v. Body: %s", status, http.StatusOK, rr.Body.String())
		}

		var response []Log
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}
		if len(response) != 2 {
			t.Errorf("got %d logs, want 2", len(response))
		}

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %s", err)
		}
	})

	// NOTE: TEST 5: DELETE LOG
	t.Run("Delete Log", func(t *testing.T) {
		handler, mock, _, cleanup := setupTestStack(t)
		defer cleanup()

		mock.ExpectExec(`DELETE FROM logs WHERE id = \$1 AND user_id = \$2`).
			WithArgs(testLogID, testUserID).
			WillReturnResult(pgxmock.NewResult("DELETE", 1))

		urlParams := map[string]string{"id": testLogID}
		req := createRequest(http.MethodDelete, fmt.Sprintf("/api/v1/logs/%s", testLogID), nil, urlParams, nil)
		rr := httptest.NewRecorder()

		handler.DeleteLog(rr, req)

		if status := rr.Code; status != http.StatusNoContent {
			t.Errorf("got %v want %v. Body: %s", status, http.StatusNoContent, rr.Body.String())
		}

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %s", err)
		}
	})

	// NOTE: TEST 6: HTML CONTENT IS PRESERVED (NOT STRIPPED)
	t.Run("HTML Content Preserved", func(t *testing.T) {
		handler, mock, _, cleanup := setupTestStack(t)
		defer cleanup()

		parsedTime, _ := time.Parse(time.RFC3339, testDateTime)
		raw := "<script>alert('XSS')</script>Normal text"

		mock.ExpectQuery(`INSERT INTO logs .+ RETURNING .+`).
			WithArgs(testUserID, testDateTime, raw).
			WillReturnRows(mockLogRows(mock).AddRow(
				testLogID, testUserID, parsedTime, raw, now, now,
			))

		logReq := CreateRequest{DateAndTime: testDateTime, Log: raw}
		reqBody, _ := json.Marshal(logReq)

		req := createRequest(http.MethodPost, "/api/v1/logs", reqBody, nil, nil)
		rr := httptest.NewRecorder()

		handler.CreateLog(rr, req)

		if status := rr.Code; status != http.StatusCreated {
			t.Errorf("got %v want %v. Body: %s", status, http.StatusCreated, rr.Body.String())
		}

		var response Log
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}
		if response.Log != raw {
			t.Errorf("HTML stripped: got %q, want %q (item 10 mandates preservation)", response.Log, raw)
		}

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %s", err)
		}
	})

	// NOTE: TEST 7: INVALID DATE_AND_TIME FORMAT → ErrInvalidDateTime counted
	t.Run("Invalid DateAndTime Format", func(t *testing.T) {
		invalidDateTimes := []string{
			"2025-12-28",
			"28-12-2025T10:00:00Z",
			"not-a-date",
		}

		for _, badDT := range invalidDateTimes {
			t.Run(fmt.Sprintf("POST with date_and_time=%s", badDT), func(t *testing.T) {
				handler, _, errCV, cleanup := setupTestStack(t)
				defer cleanup()

				logReq := CreateRequest{DateAndTime: badDT, Log: "Test log"}
				reqBody, _ := json.Marshal(logReq)

				req := createRequest(http.MethodPost, "/api/v1/logs", reqBody, nil, nil)
				rr := httptest.NewRecorder()

				handler.CreateLog(rr, req)

				if rr.Code != http.StatusBadRequest {
					t.Errorf("got %v want BadRequest for date_and_time '%s'", rr.Code, badDT)
				}
				assertErrorRecorded(t, errCV, ErrTypeInvalidDateTime, 1)

				// The message names the offending field so the client knows
				// which input was malformed (here the single-create body field).
				var env shared.ErrorResponse
				if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
					t.Fatalf("body is not a JSON envelope: %v\nbody: %s", err, rr.Body.String())
				}
				if !strings.Contains(env.Message, "date_and_time") {
					t.Errorf("message should name the date_and_time field: %q", env.Message)
				}
			})
		}
	})

	// NOTE: TEST 8: LOG NOT FOUND → ErrLogNotFound counted
	t.Run("Get Non-Existent Log", func(t *testing.T) {
		handler, mock, errCV, cleanup := setupTestStack(t)
		defer cleanup()

		mock.ExpectQuery(`SELECT .+ FROM logs WHERE id = \$1 AND user_id = \$2`).
			WithArgs(missingLogID, testUserID).
			WillReturnError(pgx.ErrNoRows)

		urlParams := map[string]string{"id": missingLogID}
		req := createRequest(http.MethodGet, fmt.Sprintf("/api/v1/logs/%s", missingLogID), nil, urlParams, nil)
		rr := httptest.NewRecorder()

		handler.GetLog(rr, req)

		if status := rr.Code; status != http.StatusNotFound {
			t.Errorf("got %v want NotFound. Body: %s", status, rr.Body.String())
		}
		assertErrorRecorded(t, errCV, ErrTypeLogNotFound, 1)

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %s", err)
		}
	})

	// NOTE: TEST 9: EMPTY LOG CONTENT → ErrValidation counted
	t.Run("Empty Log Content", func(t *testing.T) {
		handler, _, errCV, cleanup := setupTestStack(t)
		defer cleanup()

		logReq := CreateRequest{DateAndTime: testDateTime, Log: ""}
		reqBody, _ := json.Marshal(logReq)

		req := createRequest(http.MethodPost, "/api/v1/logs", reqBody, nil, nil)
		rr := httptest.NewRecorder()

		handler.CreateLog(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Errorf("got %v want BadRequest for empty log. Body: %s", rr.Code, rr.Body.String())
		}
		assertErrorRecorded(t, errCV, ErrTypeValidation, 1)
	})

	// NOTE: TEST 10: LOG TOO LONG (ASCII) → limit_exceeded counted, with the
	// structured JSON body reaching the client. The length limit is enforced
	// in the service (no rune_max struct tag), so the LimitExceededError,
	// carrying the limit and the client's current count, is returned, not a
	// generic validation 400.
	t.Run("Log Too Long", func(t *testing.T) {
		handler, _, errCV, cleanup := setupTestStack(t)
		defer cleanup()

		overBy := 1
		longLog := strings.Repeat("a", shared.LogMaxChars+overBy)

		logReq := CreateRequest{DateAndTime: testDateTime, Log: longLog}
		reqBody, _ := json.Marshal(logReq)

		req := createRequest(http.MethodPost, "/api/v1/logs", reqBody, nil, nil)
		rr := httptest.NewRecorder()

		handler.CreateLog(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Errorf("got %v want BadRequest for over-limit log", rr.Code)
		}
		if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type: got %q want application/json", ct)
		}
		assertErrorRecorded(t, errCV, ErrTypeLimitExceeded, 1)

		// The structured body must carry the bounded error type plus the limit
		// and the client's current count so the caller knows by how much they
		// exceeded it.
		var body shared.LimitExceededError
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("failed to decode limit-exceeded body: %v\nbody: %s", err, rr.Body.String())
		}
		if body.ErrorType != "limit_exceeded" {
			t.Errorf("error field: got %q want limit_exceeded", body.ErrorType)
		}
		if body.Limit != shared.LogMaxChars {
			t.Errorf("limit field: got %d want %d", body.Limit, shared.LogMaxChars)
		}
		if body.Current != shared.LogMaxChars+overBy {
			t.Errorf("current field: got %d want %d", body.Current, shared.LogMaxChars+overBy)
		}
	})

	// NOTE: TEST 11: MULTI-BYTE CHARACTERS ACCEPTED
	t.Run("Multi-byte Characters Accepted", func(t *testing.T) {
		handler, mock, _, cleanup := setupTestStack(t)
		defer cleanup()

		parsedTime, _ := time.Parse(time.RFC3339, testDateTime)

		multiByteLog := strings.Repeat("é", 100)

		mock.ExpectQuery(`INSERT INTO logs .+ RETURNING .+`).
			WithArgs(testUserID, testDateTime, multiByteLog).
			WillReturnRows(mockLogRows(mock).AddRow(
				testLogID, testUserID, parsedTime, multiByteLog, now, now,
			))

		logReq := CreateRequest{DateAndTime: testDateTime, Log: multiByteLog}
		reqBody, _ := json.Marshal(logReq)

		req := createRequest(http.MethodPost, "/api/v1/logs", reqBody, nil, nil)
		rr := httptest.NewRecorder()

		handler.CreateLog(rr, req)

		if rr.Code != http.StatusCreated {
			t.Errorf("got %v want Created for 100 runes. Body: %s", rr.Code, rr.Body.String())
		}

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %s", err)
		}
	})

	// NOTE: TEST 12: OVER-LIMIT EMOJI REJECTED → limit_exceeded counted.
	// Rune-counted (not byte-counted): each 😀 is one rune, so LogMaxChars+1
	// emoji is one rune over the limit.
	t.Run("Log Too Long Multi-byte Runes", func(t *testing.T) {
		handler, _, errCV, cleanup := setupTestStack(t)
		defer cleanup()

		longEmojiLog := strings.Repeat("😀", shared.LogMaxChars+1)

		logReq := CreateRequest{DateAndTime: testDateTime, Log: longEmojiLog}
		reqBody, _ := json.Marshal(logReq)

		req := createRequest(http.MethodPost, "/api/v1/logs", reqBody, nil, nil)
		rr := httptest.NewRecorder()

		handler.CreateLog(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Errorf("got %v want BadRequest for over-limit emoji", rr.Code)
		}
		assertErrorRecorded(t, errCV, ErrTypeLimitExceeded, 1)
	})

	// NOTE: TEST 13: EXACTLY 10000 RUNES ACCEPTED
	t.Run("Exactly 10000 Runes Accepted", func(t *testing.T) {
		handler, mock, _, cleanup := setupTestStack(t)
		defer cleanup()

		parsedTime, _ := time.Parse(time.RFC3339, testDateTime)

		maxLog := strings.Repeat("é", 10000)

		mock.ExpectQuery(`INSERT INTO logs .+ RETURNING .+`).
			WithArgs(testUserID, testDateTime, maxLog).
			WillReturnRows(mockLogRows(mock).AddRow(
				testLogID, testUserID, parsedTime, maxLog, now, now,
			))

		logReq := CreateRequest{DateAndTime: testDateTime, Log: maxLog}
		reqBody, _ := json.Marshal(logReq)

		req := createRequest(http.MethodPost, "/api/v1/logs", reqBody, nil, nil)
		rr := httptest.NewRecorder()

		handler.CreateLog(rr, req)

		if rr.Code != http.StatusCreated {
			t.Errorf("got %v want Created for exactly 10000 runes. Body: %s", rr.Code, rr.Body.String())
		}

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %s", err)
		}
	})

	// NOTE: TEST 14: NON-UUID PATH PARAM → ErrInvalidInput counted
	t.Run("Invalid UUID Path Returns 400", func(t *testing.T) {
		cases := []struct {
			name    string
			method  string
			handler func(http.ResponseWriter, *http.Request)
			body    []byte
		}{
			{"GET", http.MethodGet, nil, nil},
			{"PUT", http.MethodPut, nil, []byte(`{"log":"x"}`)},
			{"DELETE", http.MethodDelete, nil, nil},
		}
		for i := range cases {
			c := cases[i]
			t.Run(c.name, func(t *testing.T) {
				handler, _, errCV, cleanup := setupTestStack(t)
				defer cleanup()

				switch c.method {
				case http.MethodGet:
					c.handler = handler.GetLog
				case http.MethodPut:
					c.handler = handler.UpdateLog
				case http.MethodDelete:
					c.handler = handler.DeleteLog
				}

				urlParams := map[string]string{"id": "not-a-uuid"}
				req := createRequest(c.method, "/api/v1/logs/not-a-uuid", c.body, urlParams, nil)
				rr := httptest.NewRecorder()

				c.handler(rr, req)

				if rr.Code != http.StatusBadRequest {
					t.Errorf("%s /logs/not-a-uuid: got %v want BadRequest. Body: %s",
						c.method, rr.Code, rr.Body.String())
				}
				assertErrorRecorded(t, errCV, ErrTypeInvalidInput, 1)
			})
		}
	})

	// NOTE: TEST 15: INVALID PAGINATION → ErrInvalidPagination counted
	t.Run("Invalid Pagination Returns 400", func(t *testing.T) {
		handler, _, errCV, cleanup := setupTestStack(t)
		defer cleanup()

		queryParams := map[string]string{"limit": "not-a-number"}
		req := createRequest(http.MethodGet, "/api/v1/logs", nil, nil, queryParams)
		rr := httptest.NewRecorder()

		handler.ListLogs(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Errorf("got %v want BadRequest. Body: %s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "invalid pagination") {
			t.Errorf("response body should mention pagination, got: %s", rr.Body.String())
		}
		assertErrorRecorded(t, errCV, ErrTypeInvalidPagination, 1)
	})

	// NOTE: TEST 15b: INVALID start_date ON LIST → ErrInvalidDateTime counted,
	// and the message names WHICH bound failed so the operator/client can tell a
	// bad start_date from a bad end_date.
	t.Run("Invalid start_date Names The Field", func(t *testing.T) {
		handler, _, errCV, cleanup := setupTestStack(t)
		defer cleanup()

		queryParams := map[string]string{"start_date": "not-a-date"}
		req := createRequest(http.MethodGet, "/api/v1/logs", nil, nil, queryParams)
		rr := httptest.NewRecorder()

		handler.ListLogs(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Errorf("got %v want BadRequest. Body: %s", rr.Code, rr.Body.String())
		}
		assertErrorRecorded(t, errCV, ErrTypeInvalidDateTime, 1)

		var env shared.ErrorResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
			t.Fatalf("body is not a JSON envelope: %v\nbody: %s", err, rr.Body.String())
		}
		if !strings.Contains(env.Message, "start_date") {
			t.Errorf("message should name the offending bound start_date: %q", env.Message)
		}
		if strings.Contains(env.Message, "end_date") {
			t.Errorf("message must not name end_date when start_date failed: %q", env.Message)
		}
	})

	// NOTE: TEST 16: UNAUTHORISED (no user ID in context) → ErrUnauthorised counted
	t.Run("Unauthorised Returns 401", func(t *testing.T) {
		handler, _, errCV, cleanup := setupTestStack(t)
		defer cleanup()

		req := createRequestNoUser(http.MethodGet, "/api/v1/logs", nil, nil)
		rr := httptest.NewRecorder()

		handler.ListLogs(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("got %v want Unauthorized. Body: %s", rr.Code, rr.Body.String())
		}
		assertErrorRecorded(t, errCV, ErrTypeUnauthorised, 1)
	})

	// NOTE: TEST 17: DATABASE ERROR → ErrDatabase counted (returns 500)
	t.Run("Database Error Returns 500", func(t *testing.T) {
		handler, mock, errCV, cleanup := setupTestStack(t)
		defer cleanup()

		mock.ExpectQuery(`SELECT .+ FROM logs WHERE id = \$1 AND user_id = \$2`).
			WithArgs(testLogID, testUserID).
			WillReturnError(errors.New("connection reset by peer"))

		urlParams := map[string]string{"id": testLogID}
		req := createRequest(http.MethodGet, fmt.Sprintf("/api/v1/logs/%s", testLogID), nil, urlParams, nil)
		rr := httptest.NewRecorder()

		handler.GetLog(rr, req)

		if rr.Code != http.StatusInternalServerError {
			t.Errorf("got %v want InternalServerError. Body: %s", rr.Code, rr.Body.String())
		}
		assertErrorRecorded(t, errCV, ErrTypeDatabase, 1)
	})

	// NOTE: TEST 18: MALFORMED JSON BODY → ErrInvalidReqBody counted
	t.Run("Malformed JSON Body Returns 400", func(t *testing.T) {
		handler, _, errCV, cleanup := setupTestStack(t)
		defer cleanup()

		req := createRequest(http.MethodPost, "/api/v1/logs", []byte(`{not json`), nil, nil)
		rr := httptest.NewRecorder()

		handler.CreateLog(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Errorf("got %v want BadRequest. Body: %s", rr.Code, rr.Body.String())
		}
		assertErrorRecorded(t, errCV, ErrTypeInvalidReqBody, 1)
	})
}

// TestHandler_NonUUIDUserReturns401 verifies the UUID user-ID contract at the
// handler boundary: a request whose context carries a non-UUID subject (an
// unmapped OAuth `sub`) is rejected as 401 in the envelope, error_type is
// "unauthorised" (NOT "database"), and NO query reaches the pool; the
// misconfigured integration is never misfiled as a database fault.
func TestHandler_NonUUIDUserReturns401(t *testing.T) {
	handler, mock, cv, cleanup := setupTestStack(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs", nil)
	req = req.WithContext(shared.WithUserID(req.Context(), "auth0|12345"))
	rr := httptest.NewRecorder()

	handler.ListLogs(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("got %d want 401. Body: %s", rr.Code, rr.Body.String())
	}
	if w := rr.Header().Get("WWW-Authenticate"); w == "" {
		t.Error("401 must carry the WWW-Authenticate header")
	}
	var env shared.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("body is not a JSON envelope: %v\nbody: %s", err, rr.Body.String())
	}
	if env.ErrorType != "unauthorised" {
		t.Errorf("error field: got %q want unauthorised", env.ErrorType)
	}
	assertErrorRecorded(t, cv, ErrTypeUnauthorised, 1)
	// The non-UUID subject must NOT be recorded as a database fault.
	assertErrorRecorded(t, cv, ErrTypeDatabase, 0)
	// No query may have reached the mock pool (the mock has zero expectations;
	// had a query run, pgxmock would have flagged it as unexpected).
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB interaction on the unauthorised path: %v", err)
	}
}

// TestPathUUIDCanonicalised verifies that UUID path params in non-canonical
// (but uuid.Parse-accepted) forms (the `urn:uuid:` prefix and `{...}` braces,
// both of which Postgres' uuid type rejects) are canonicalised before reaching
// the repository. The request succeeds and the DB sees the bare canonical UUID,
// so a 500 from a Postgres syntax error is impossible.
func TestPathUUIDCanonicalised(t *testing.T) {
	cases := []struct {
		name  string
		rawID string
	}{
		{"urn prefix", "urn:uuid:" + testLogID},
		{"brace wrapped", "{" + testLogID + "}"},
		{"uppercase", strings.ToUpper(testLogID)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			handler, mock, _, cleanup := setupTestStack(t)
			defer cleanup()

			now := time.Now()
			parsedTime, _ := time.Parse(time.RFC3339, "2025-12-28T10:30:00Z")

			// The repository must receive the canonical lowercase UUID, NOT the
			// decorated/uppercased raw path value.
			mock.ExpectQuery(`SELECT .+ FROM logs WHERE id = \$1 AND user_id = \$2`).
				WithArgs(testLogID, testUserID).
				WillReturnRows(mockLogRows(mock).AddRow(
					testLogID, testUserID, parsedTime, "entry", now, now,
				))

			urlParams := map[string]string{"id": c.rawID}
			req := createRequest(http.MethodGet, "/api/v1/logs/"+c.rawID, nil, urlParams, nil)
			rr := httptest.NewRecorder()

			handler.GetLog(rr, req)

			if rr.Code != http.StatusOK {
				t.Errorf("got %d want 200 (canonicalised). Body: %s", rr.Code, rr.Body.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("repo did not receive the canonical UUID: %s", err)
			}
		})
	}
}

// TestDecodeJSONBodyStrict verifies the strict JSON decode hardening: an
// unknown field and trailing data after the first object are both rejected as
// 400 invalid_request_body (not silently ignored).
func TestDecodeJSONBodyStrict(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "unknown field",
			body: `{"date_and_time":"2025-12-28T10:30:00Z","log":"x","bogus":1}`,
		},
		{
			name: "trailing second object",
			body: `{"date_and_time":"2025-12-28T10:30:00Z","log":"x"}{"log":"y"}`,
		},
		{
			name: "trailing garbage",
			body: `{"date_and_time":"2025-12-28T10:30:00Z","log":"x"} not-json`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			handler, _, errCV, cleanup := setupTestStack(t)
			defer cleanup()

			req := createRequest(http.MethodPost, "/api/v1/logs", []byte(c.body), nil, nil)
			rr := httptest.NewRecorder()

			handler.CreateLog(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Errorf("got %d want 400. Body: %s", rr.Code, rr.Body.String())
			}
			assertErrorRecorded(t, errCV, ErrTypeInvalidReqBody, 1)

			var env shared.ErrorResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
				t.Fatalf("body is not a JSON error envelope: %v\nbody: %s", err, rr.Body.String())
			}
			if env.ErrorType != "invalid_request_body" {
				t.Errorf("error field: got %q want invalid_request_body", env.ErrorType)
			}
		})
	}
}

// TestRequestBodyTooLarge verifies that a body over the 1 MiB cap surfaces as
// 413 (not 400) in the standard error envelope. The request is driven through a
// router that includes chimw.RequestSize(1<<20), the same middleware main.go
// wires, so r.Body is wrapped in an http.MaxBytesReader and decode returns an
// *http.MaxBytesError, which the handler distinguishes from a malformed body.
func TestRequestBodyTooLarge(t *testing.T) {
	const bodyCap = 1 << 20 // 1 MiB, matching cmd/api/main.go

	// A log payload comfortably over the cap. Valid JSON shape so the ONLY
	// failure mode exercised is the size limit.
	big := strings.Repeat("a", bodyCap+1024)

	cases := []struct {
		name   string
		method string
		path   string
		body   string
		route  func(r chi.Router, h *Handler)
	}{
		{
			name:   "POST",
			method: http.MethodPost,
			path:   "/api/v1/logs",
			body:   `{"date_and_time":"2025-12-28T10:30:00Z","log":"` + big + `"}`,
			route:  func(r chi.Router, h *Handler) { r.Post("/api/v1/logs", h.CreateLog) },
		},
		{
			name:   "PUT",
			method: http.MethodPut,
			path:   "/api/v1/logs/" + testLogID,
			body:   `{"log":"` + big + `"}`,
			route:  func(r chi.Router, h *Handler) { r.Put("/api/v1/logs/{id}", h.UpdateLog) },
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			handler, _, errCV, cleanup := setupTestStack(t)
			defer cleanup()

			r := chi.NewRouter()
			r.Use(chimw.RequestSize(bodyCap))
			c.route(r, handler)

			req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(shared.WithUserID(req.Context(), testUserID))
			rr := httptest.NewRecorder()

			r.ServeHTTP(rr, req)

			if rr.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("got %d want 413 Content Too Large. Body: %s", rr.Code, rr.Body.String())
			}
			if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type: got %q want application/json", ct)
			}
			assertErrorRecorded(t, errCV, ErrTypeBodyTooLarge, 1)

			var env shared.ErrorResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
				t.Fatalf("body is not a JSON error envelope: %v\nbody: %s", err, rr.Body.String())
			}
			if env.ErrorType != "body_too_large" {
				t.Errorf("error field: got %q want body_too_large", env.ErrorType)
			}
		})
	}
}

// ============================================================================
// BATCH CREATE (atomic multi-statement transaction)
// ============================================================================

const batchDateTime = "2025-12-28T10:30:00Z"

// batchBody builds a {"logs":[...]} request body with the given log contents,
// each carrying batchDateTime.
func batchBody(contents ...string) []byte {
	logs := make([]CreateRequest, len(contents))
	for i, c := range contents {
		logs[i] = CreateRequest{DateAndTime: batchDateTime, Log: c}
	}
	body, _ := json.Marshal(BatchCreateRequest{Logs: logs})
	return body
}

// TestBatchCreate_HappyPathCommits verifies all entries are created inside one
// transaction (Begin → N inserts → Commit) and returned in request order.
func TestBatchCreate_HappyPathCommits(t *testing.T) {
	handler, mock, _, cleanup := setupTestStack(t)
	defer cleanup()

	now := time.Now()
	parsed, _ := time.Parse(time.RFC3339, batchDateTime)
	contents := []string{"first", "second", "third"}

	mock.ExpectBegin()
	for i, c := range contents {
		mock.ExpectQuery(`INSERT INTO logs .+ RETURNING .+`).
			WithArgs(testUserID, batchDateTime, c).
			WillReturnRows(mockLogRows(mock).AddRow(
				fmt.Sprintf("660e8400-e29b-41d4-a716-44665544000%d", i),
				testUserID, parsed, c, now, now))
	}
	mock.ExpectCommit()

	req := createRequest(http.MethodPost, "/api/v1/logs/batch", batchBody(contents...), nil, nil)
	rr := httptest.NewRecorder()
	handler.BatchCreate(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("got %d want 201. Body: %s", rr.Code, rr.Body.String())
	}
	var got []Log
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entries want 3", len(got))
	}
	for i, c := range contents {
		if got[i].Log != c {
			t.Errorf("order: entry %d = %q want %q", i, got[i].Log, c)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %v", err)
	}
}

// TestBatchCreate_FailingInsertRollsBack verifies that a failure on the third
// insert rolls back the whole batch (Begin → 2 inserts → failing insert →
// Rollback, NO Commit) and surfaces as a bounded database error.
func TestBatchCreate_FailingInsertRollsBack(t *testing.T) {
	handler, mock, cv, cleanup := setupTestStack(t)
	defer cleanup()

	now := time.Now()
	parsed, _ := time.Parse(time.RFC3339, batchDateTime)

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO logs .+ RETURNING .+`).
		WithArgs(testUserID, batchDateTime, "ok1").
		WillReturnRows(mockLogRows(mock).AddRow(testLogID, testUserID, parsed, "ok1", now, now))
	mock.ExpectQuery(`INSERT INTO logs .+ RETURNING .+`).
		WithArgs(testUserID, batchDateTime, "ok2").
		WillReturnRows(mockLogRows(mock).AddRow(missingLogID, testUserID, parsed, "ok2", now, now))
	mock.ExpectQuery(`INSERT INTO logs .+ RETURNING .+`).
		WithArgs(testUserID, batchDateTime, "boom").
		WillReturnError(errors.New("simulated insert failure"))
	mock.ExpectRollback()

	req := createRequest(http.MethodPost, "/api/v1/logs/batch", batchBody("ok1", "ok2", "boom"), nil, nil)
	rr := httptest.NewRecorder()
	handler.BatchCreate(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("got %d want 500. Body: %s", rr.Code, rr.Body.String())
	}
	assertErrorRecorded(t, cv, ErrTypeDatabase, 1)
	// ExpectationsWereMet enforces that Rollback (not Commit) ran.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expected rollback, unfulfilled expectations: %v", err)
	}
}

// TestBatchCreate_OverLimitRejectedBeforeDB verifies an over-size batch is
// rejected with the structured limit_exceeded envelope BEFORE any DB call (the
// mock has zero expectations, so a Begin would be flagged as unexpected).
func TestBatchCreate_OverLimitRejectedBeforeDB(t *testing.T) {
	handler, mock, cv, cleanup := setupTestStack(t)
	defer cleanup()

	contents := make([]string, shared.MaxBatchSize+1)
	for i := range contents {
		contents[i] = "x"
	}

	req := createRequest(http.MethodPost, "/api/v1/logs/batch", batchBody(contents...), nil, nil)
	rr := httptest.NewRecorder()
	handler.BatchCreate(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400. Body: %s", rr.Code, rr.Body.String())
	}
	var env shared.LimitExceededError
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.ErrorType != "limit_exceeded" {
		t.Errorf("error: got %q want limit_exceeded", env.ErrorType)
	}
	if env.Limit != shared.MaxBatchSize || env.Current != shared.MaxBatchSize+1 {
		t.Errorf("limit/current: got %d/%d want %d/%d", env.Limit, env.Current, shared.MaxBatchSize, shared.MaxBatchSize+1)
	}
	assertErrorRecorded(t, cv, ErrTypeLimitExceeded, 1)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("over-limit batch must not touch the DB: %v", err)
	}
}

// TestBatchCreate_EmptyArrayRejected verifies an empty list is a 400 validation
// error (the `required` tag on Logs), before any DB call.
func TestBatchCreate_EmptyArrayRejected(t *testing.T) {
	handler, _, cv, cleanup := setupTestStack(t)
	defer cleanup()

	req := createRequest(http.MethodPost, "/api/v1/logs/batch", []byte(`{"logs":[]}`), nil, nil)
	rr := httptest.NewRecorder()
	handler.BatchCreate(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400. Body: %s", rr.Code, rr.Body.String())
	}
	assertErrorRecorded(t, cv, ErrTypeValidation, 1)
}

// TestBatchCreate_BadDateNamesIndex verifies a bad-date entry surfaces its batch
// index to the client (parity with the over-length error), while staying the
// bounded invalid_datetime type and keeping the RFC3339 hint.
func TestBatchCreate_BadDateNamesIndex(t *testing.T) {
	handler, _, cv, cleanup := setupTestStack(t)
	defer cleanup()

	body, _ := json.Marshal(BatchCreateRequest{Logs: []CreateRequest{
		{DateAndTime: "2025-12-28T10:30:00Z", Log: "a"},
		{DateAndTime: "2025-12-28T10:30:00Z", Log: "b"},
		{DateAndTime: "not-a-date", Log: "c"}, // index 2, the failing entry
	}})
	req := createRequest(http.MethodPost, "/api/v1/logs/batch", body, nil, nil)
	rr := httptest.NewRecorder()
	handler.BatchCreate(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400. Body: %s", rr.Code, rr.Body.String())
	}
	assertErrorRecorded(t, cv, ErrTypeInvalidDateTime, 1)

	var env shared.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("body is not a JSON envelope: %v\nbody: %s", err, rr.Body.String())
	}
	if env.ErrorType != "invalid_datetime" {
		t.Errorf("error field: got %q want invalid_datetime", env.ErrorType)
	}
	if !strings.Contains(env.Message, "entry 2") {
		t.Errorf("message should name the failing entry index 2: %q", env.Message)
	}
	if !strings.Contains(env.Message, "date_and_time") {
		t.Errorf("message should name the offending field date_and_time: %q", env.Message)
	}
	if !strings.Contains(env.Message, "RFC3339") {
		t.Errorf("message should keep the RFC3339 hint from the sentinel: %q", env.Message)
	}
}

// TestInvalidDateTimeError verifies the structured error matches the sentinel
// via errors.Is and composes its message correctly (index named only for
// batch entries).
func TestInvalidDateTimeError(t *testing.T) {
	if !errors.Is(&InvalidDateTimeError{Index: 2}, ErrInvalidDateTime) {
		t.Error("batch InvalidDateTimeError should match ErrInvalidDateTime via Is")
	}
	if !errors.Is(&InvalidDateTimeError{Index: -1}, ErrInvalidDateTime) {
		t.Error("non-batch InvalidDateTimeError should match ErrInvalidDateTime via Is")
	}
	if got := (&InvalidDateTimeError{Index: 2}).Error(); !strings.Contains(got, "entry 2") {
		t.Errorf("batch message should name the index: %q", got)
	}
	if got := (&InvalidDateTimeError{Index: -1}).Error(); strings.Contains(got, "entry") {
		t.Errorf("non-batch message must not name an index: %q", got)
	}

	// Field prefix: a non-batch error names the offending field, then the
	// sentinel text, and never an entry index.
	if got := (&InvalidDateTimeError{Field: "start_date", Index: -1}).Error(); !strings.HasPrefix(got, "start_date: ") {
		t.Errorf("field error should start with the field name: %q", got)
	}
	if got := (&InvalidDateTimeError{Field: "start_date", Index: -1}).Error(); !strings.Contains(got, "RFC3339") {
		t.Errorf("field error should keep the RFC3339 hint: %q", got)
	}
	if got := (&InvalidDateTimeError{Field: "end_date", Index: -1}).Error(); strings.Contains(got, "entry") {
		t.Errorf("non-batch field error must not name an index: %q", got)
	}

	// Combined "entry N: <field>: " form for a batch entry.
	if got := (&InvalidDateTimeError{Field: "date_and_time", Index: 2}).Error(); !strings.HasPrefix(got, "entry 2: date_and_time: ") {
		t.Errorf("batch field error should read 'entry N: <field>: ...': %q", got)
	}
}

// TestRepositoryPreservesUnderlyingError (item 5 of the previous batch)
// verifies the multi-%w wrapping in the repository: errors.Is still matches
// ErrDatabase AND the underlying driver error text is recoverable through
// Error()/Unwrap().
func TestRepositoryPreservesUnderlyingError(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("Failed to create pgxmock pool: %v", err)
	}
	defer mock.Close()

	driverErr := errors.New("simulated pgx connection refused")
	mock.ExpectQuery(`SELECT .+ FROM logs WHERE id = \$1 AND user_id = \$2`).
		WithArgs(testLogID, testUserID).
		WillReturnError(driverErr)

	repo := NewLogRepository(mock)
	_, err = repo.getLog(context.Background(), testLogID, testUserID)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.Is(err, ErrDatabase) {
		t.Errorf("expected errors.Is(err, ErrDatabase) to be true, got: %v", err)
	}
	if !strings.Contains(err.Error(), driverErr.Error()) {
		t.Errorf("expected error text to contain underlying driver error %q, got: %q",
			driverErr.Error(), err.Error())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("Unfulfilled expectations: %s", err)
	}
}

// TestLimitExceededErrorAsType verifies the LimitExceededError path is
// reachable via errors.As (used by the handler).
func TestLimitExceededErrorAsType(t *testing.T) {
	wrapped := fmt.Errorf("wrap: %w", NewLogTooLongError(11000))
	var limitErr *shared.LimitExceededError
	if !errors.As(wrapped, &limitErr) {
		t.Fatal("errors.As should match LimitExceededError")
	}
	if limitErr.Limit != shared.LogMaxChars || limitErr.Current != 11000 {
		t.Errorf("unexpected fields: %+v", limitErr)
	}
}
