//go:build integration

// Integration tests that exercise the repository against a real Postgres.
// Run with: make test-integration  (requires `make run` so the dev database
// is up and migrated).
//
// Connection details come from the same env vars the application uses:
// DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, DB_NAME, DB_SSLMODE.

package userlog

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sud0x0/go-api-template/internal/config"
	"github.com/sud0x0/go-api-template/internal/db"
	"github.com/sud0x0/go-api-template/internal/shared"
	"github.com/sud0x0/go-api-template/internal/shared/logger"
)

type integrationLogger struct{}

func (m *integrationLogger) LogError(simplifiedError, actualError error)              {}
func (m *integrationLogger) LogWarn(message string, args ...any)                      {}
func (m *integrationLogger) LogInfo(message string, args ...any)                      {}
func (m *integrationLogger) LogDebug(message string, args ...any)                     {}
func (m *integrationLogger) WithRequestContext(_ logger.RequestContext) logger.Logger { return m }

// noopHandlerMetrics is a HandlerMetrics that discards calls, used by
// integration tests that don't care about the api_errors_total counter.
type noopHandlerMetrics struct{}

func (noopHandlerMetrics) IncAPIError(_, _ string) {}

func mustEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("integration test requires %s (run `make run` first)", key)
	}
	return v
}

func setupIntegrationPool(t *testing.T) *pgxpool.Pool {
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestLogRepository_CRUD_Integration(t *testing.T) {
	pool := setupIntegrationPool(t)
	repo := NewLogRepository(pool)

	userID := "99999999-9999-9999-9999-999999999999"
	ctx := context.Background()

	dt := "2025-01-15T10:00:00Z"
	created, err := repo.createLog(ctx, userID, dt, "integration test entry")
	if err != nil {
		t.Fatalf("createLog: %v", err)
	}
	if created.UserID != userID || created.Log != "integration test entry" {
		t.Errorf("created log fields mismatch: %+v", created)
	}

	t.Cleanup(func() {
		_ = repo.deleteLog(context.Background(), created.ID, userID)
	})

	got, err := repo.getLog(ctx, created.ID, userID)
	if err != nil {
		t.Fatalf("getLog: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("getLog id mismatch: got %s want %s", got.ID, created.ID)
	}

	updated, err := repo.updateLog(ctx, created.ID, userID, "updated content")
	if err != nil {
		t.Fatalf("updateLog: %v", err)
	}
	if updated.Log != "updated content" {
		t.Errorf("updated content mismatch: got %q", updated.Log)
	}

	// Prove the update_logs_updated_at trigger sets updated_at = NOW() on UPDATE,
	// deterministically, no sleep, no comparison of two near-simultaneous
	// wall-clock timestamps. The trigger fires on UPDATE, not INSERT, so a direct
	// INSERT with a fixed, long-past sentinel updated_at makes the sentinel stick;
	// a real update through the repository must then move updated_at OFF it. The
	// assertion compares against a constant (year 2000), so it never depends on
	// elapsed real time.
	const sentinel = "2000-01-01T00:00:00Z"
	sentinelTime, err := time.Parse(time.RFC3339, sentinel)
	if err != nil {
		t.Fatalf("parse sentinel: %v", err)
	}
	dtTime, err := time.Parse(time.RFC3339, dt)
	if err != nil {
		t.Fatalf("parse dt: %v", err)
	}
	var seededID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO logs (user_id, date_and_time, log, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $4) RETURNING id`,
		userID, dtTime, "seed for updated_at trigger proof", sentinelTime,
	).Scan(&seededID); err != nil {
		t.Fatalf("seed insert: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM logs WHERE id = $1", seededID)
	})
	seeded, err := repo.updateLog(ctx, seededID, userID, "trigger moves updated_at")
	if err != nil {
		t.Fatalf("updateLog (seeded row): %v", err)
	}
	if !seeded.UpdatedAt.After(sentinelTime) {
		t.Errorf("updated_at did not move off the seeded sentinel: got %s want a time after %s; the update_logs_updated_at trigger may be missing or the migration drifted",
			seeded.UpdatedAt.Format(time.RFC3339), sentinel)
	}

	logs, err := repo.getLogs(ctx, userID,
		"1970-01-01T00:00:00Z", "2099-12-31T23:59:59Z", 100, 0)
	if err != nil {
		t.Fatalf("getLogs: %v", err)
	}
	found := false
	for _, l := range logs {
		if l.ID == created.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("created log not returned by getLogs")
	}

	if err := repo.deleteLog(ctx, created.ID, userID); err != nil {
		t.Fatalf("deleteLog: %v", err)
	}

	_, err = repo.getLog(ctx, created.ID, userID)
	if err == nil {
		t.Errorf("expected ErrLogNotFound after delete, got nil")
	}
}

// TestLogHandler_InvalidUUID_Integration verifies the end-to-end 400 path
// from item 6: an invalid UUID never reaches the database.
func TestLogHandler_InvalidUUID_Integration(t *testing.T) {
	pool := setupIntegrationPool(t)
	log := &integrationLogger{}
	repo := NewLogRepository(pool)
	// This path 400s on the invalid UUID before any service/tx work, so a nil
	// txRunner is never dereferenced.
	service := NewLogService(repo, nil)
	handler := NewLogHandler(service, log, &noopHandlerMetrics{})

	userID := "550e8400-e29b-41d4-a716-446655440099"
	updateBody, err := json.Marshal(UpdateRequest{Log: "x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	cases := []struct {
		name    string
		method  string
		body    []byte
		handler func(http.ResponseWriter, *http.Request)
	}{
		{"GET", http.MethodGet, nil, handler.GetLog},
		{"PUT", http.MethodPut, updateBody, handler.UpdateLog},
		{"DELETE", http.MethodDelete, nil, handler.DeleteLog},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var reqBody *bytes.Reader
			if c.body != nil {
				reqBody = bytes.NewReader(c.body)
			} else {
				reqBody = bytes.NewReader(nil)
			}
			req := httptest.NewRequest(c.method, "/api/v1/logs/not-a-uuid", reqBody)
			req.Header.Set("Content-Type", "application/json")

			ctx := shared.WithUserID(req.Context(), userID)
			chiCtx := chi.NewRouteContext()
			chiCtx.URLParams.Add("id", "not-a-uuid")
			ctx = context.WithValue(ctx, chi.RouteCtxKey, chiCtx)
			req = req.WithContext(ctx)

			rr := httptest.NewRecorder()
			c.handler(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Errorf("%s /logs/not-a-uuid: got %d want 400", c.method, rr.Code)
			}
		})
	}
}

// setupIntegrationDB builds a real *db.DB (exercising the production
// db.WithTransaction) from the same DB_* env the pool helper uses.
func setupIntegrationDB(t *testing.T) *db.DB {
	t.Helper()
	_ = mustEnv(t, "DB_HOST") // skip early if the integration env is absent
	dbCfg, err := config.LoadDatabase()
	if err != nil {
		t.Fatalf("config.LoadDatabase: %v", err)
	}
	database, err := db.New(dbCfg, &integrationLogger{}, nil, RequiredTables)
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(database.Close)
	return database
}

func countLogsForUser(t *testing.T, database *db.DB, userID string) int {
	t.Helper()
	var n int
	if err := database.Pool().
		QueryRow(context.Background(), "SELECT count(*) FROM logs WHERE user_id = $1", userID).
		Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}

// TestLogService_BatchCreate_AtomicCommit_Integration verifies a batch of valid
// entries is created atomically against real Postgres and returned in order.
func TestLogService_BatchCreate_AtomicCommit_Integration(t *testing.T) {
	database := setupIntegrationDB(t)
	svc := NewLogService(NewLogRepository(database.Pool()), database)

	userID := "11111111-1111-1111-1111-111111111111"
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = database.Pool().Exec(ctx, "DELETE FROM logs WHERE user_id = $1", userID)
	})

	reqs := []CreateRequest{
		{DateAndTime: "2025-01-15T10:00:00Z", Log: "batch-a"},
		{DateAndTime: "2025-01-15T10:01:00Z", Log: "batch-b"},
		{DateAndTime: "2025-01-15T10:02:00Z", Log: "batch-c"},
	}
	created, err := svc.createLogs(ctx, userID, reqs)
	if err != nil {
		t.Fatalf("createLogs: %v", err)
	}
	if len(created) != 3 {
		t.Fatalf("got %d created want 3", len(created))
	}
	for i, want := range []string{"batch-a", "batch-b", "batch-c"} {
		if created[i].Log != want {
			t.Errorf("order: created[%d] = %q want %q", i, created[i].Log, want)
		}
	}
	if n := countLogsForUser(t, database, userID); n != 3 {
		t.Errorf("rows for user: got %d want 3", n)
	}
}

// TestLogService_BatchCreate_InvalidEntryLeavesZeroRows_Integration verifies an
// invalid entry (bad RFC3339 date) is rejected by up-front validation, so the
// transaction is never opened and zero rows are created.
func TestLogService_BatchCreate_InvalidEntryLeavesZeroRows_Integration(t *testing.T) {
	database := setupIntegrationDB(t)
	svc := NewLogService(NewLogRepository(database.Pool()), database)

	userID := "22222222-2222-2222-2222-222222222222"
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = database.Pool().Exec(ctx, "DELETE FROM logs WHERE user_id = $1", userID)
	})

	reqs := []CreateRequest{
		{DateAndTime: "2025-01-15T10:00:00Z", Log: "ok1"},
		{DateAndTime: "2025-01-15T10:01:00Z", Log: "ok2"},
		{DateAndTime: "not-a-date", Log: "bad"},
	}
	if _, err := svc.createLogs(ctx, userID, reqs); err == nil {
		t.Fatal("expected an error for the invalid date entry")
	}
	if n := countLogsForUser(t, database, userID); n != 0 {
		t.Errorf("invalid batch must leave 0 rows, got %d", n)
	}
}

// TestLogService_BatchCreate_RollsBackOnDBError_Integration verifies a failure
// that occurs MID-transaction (the third insert is rejected by Postgres because
// the log contains a NUL byte, which TEXT cannot store) rolls back the whole
// batch, proving atomicity at the database layer, not just up-front validation.
// This is the Task 2 "kill the third insert → zero rows" proof.
func TestLogService_BatchCreate_RollsBackOnDBError_Integration(t *testing.T) {
	database := setupIntegrationDB(t)
	svc := NewLogService(NewLogRepository(database.Pool()), database)

	userID := "33333333-3333-3333-3333-333333333333"
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = database.Pool().Exec(ctx, "DELETE FROM logs WHERE user_id = $1", userID)
	})

	// The NUL byte passes app validation (it counts as one rune; the date is
	// valid) but Postgres rejects it on INSERT, a deterministic mid-transaction
	// failure on the third statement.
	reqs := []CreateRequest{
		{DateAndTime: "2025-01-15T10:00:00Z", Log: "ok1"},
		{DateAndTime: "2025-01-15T10:01:00Z", Log: "ok2"},
		{DateAndTime: "2025-01-15T10:02:00Z", Log: "bad\x00null"},
	}
	if _, err := svc.createLogs(ctx, userID, reqs); err == nil {
		t.Fatal("expected the NUL-byte insert to fail")
	}
	if n := countLogsForUser(t, database, userID); n != 0 {
		t.Errorf("rollback failed: %d rows remain for user, want 0", n)
	}
}

// walkCursor drives getLogsCursor to exhaustion with the given page size,
// returning the ids seen in order and failing on any duplicate. onFirstPage, if
// non-nil, runs after the first page is fetched (used to inject a concurrent
// insert mid-walk).
func walkCursor(t *testing.T, svc *defaultLogService, userID string, limit int, onFirstPage func()) []string {
	t.Helper()
	ctx := context.Background()
	var order []string
	seen := map[string]bool{}
	cursor := ""
	for i := 0; ; i++ {
		if i > 1000 {
			t.Fatal("cursor walk did not terminate (possible cursor bug)")
		}
		page, next, err := svc.getLogsCursor(ctx, userID, "", "", cursor, limit)
		if err != nil {
			t.Fatalf("getLogsCursor: %v", err)
		}
		for _, l := range page {
			if seen[l.ID] {
				t.Errorf("duplicate row %s across cursor pages", l.ID)
			}
			seen[l.ID] = true
			order = append(order, l.ID)
		}
		if i == 0 && onFirstPage != nil {
			onFirstPage()
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return order
}

// TestLogService_CursorWalk_TieBreak_Integration inserts rows with deliberate
// timestamp ties, walks every page via next_cursor with a small page size, and
// asserts the union equals the inserted set EXACTLY ONCE (no gaps, no
// duplicates) across boundaries where rows share a date_and_time. This is the
// Task 3 tie-break proof; it fails if the cursor drops the id tiebreaker.
func TestLogService_CursorWalk_TieBreak_Integration(t *testing.T) {
	database := setupIntegrationDB(t)
	repo := NewLogRepository(database.Pool())
	svc := NewLogService(repo, database)

	userID := "44444444-4444-4444-4444-444444444444"
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = database.Pool().Exec(ctx, "DELETE FROM logs WHERE user_id = $1", userID)
	})

	inserted := map[string]bool{}
	insert := func(dt string) {
		l, err := repo.createLog(ctx, userID, dt, "x")
		if err != nil {
			t.Fatalf("seed insert: %v", err)
		}
		inserted[l.ID] = true
	}
	// 3 rows tie on the later timestamp, 2 tie on the earlier: 5 across page
	// boundaries with a page size of 2, so ties straddle boundaries.
	for range 3 {
		insert("2025-03-01T10:00:00Z")
	}
	for range 2 {
		insert("2025-03-01T09:00:00Z")
	}

	order := walkCursor(t, svc, userID, 2, nil)

	if len(order) != len(inserted) {
		t.Fatalf("walk yielded %d rows want %d (gaps or duplicates)", len(order), len(inserted))
	}
	for id := range inserted {
		found := false
		for _, got := range order {
			if got == id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("inserted row %s never appeared in the walk", id)
		}
	}
}

// TestLogService_CursorWalk_ConcurrentInsert_Integration walks the list while a
// new (older) row is inserted mid-walk, and asserts the keyset walk never
// returns a duplicate, the property offset pagination cannot guarantee under
// concurrent inserts. The mid-walk row sorts into the not-yet-walked region, so
// it also appears.
func TestLogService_CursorWalk_ConcurrentInsert_Integration(t *testing.T) {
	database := setupIntegrationDB(t)
	repo := NewLogRepository(database.Pool())
	svc := NewLogService(repo, database)

	userID := "55555555-5555-5555-5555-555555555555"
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = database.Pool().Exec(ctx, "DELETE FROM logs WHERE user_id = $1", userID)
	})

	for i := range 4 {
		// Descending timestamps so the walk order is deterministic.
		dt := time.Date(2025, 3, 1, 12-i, 0, 0, 0, time.UTC).Format(time.RFC3339)
		if _, err := repo.createLog(ctx, userID, dt, "x"); err != nil {
			t.Fatalf("seed insert: %v", err)
		}
	}

	var midID string
	order := walkCursor(t, svc, userID, 2, func() {
		// Insert a row OLDER than all existing (sorts after the first page in
		// DESC order), so it is ahead of the cursor and must appear without
		// duplicating any already-returned row.
		l, err := repo.createLog(ctx, userID, "2025-03-01T01:00:00Z", "inserted-mid-walk")
		if err != nil {
			t.Fatalf("mid-walk insert: %v", err)
		}
		midID = l.ID
	})

	// walkCursor already fails on any duplicate; assert the mid-walk row appeared.
	found := false
	for _, id := range order {
		if id == midID {
			found = true
		}
	}
	if !found {
		t.Errorf("mid-walk inserted row %s (older than the cursor) should have appeared", midID)
	}
}

// driveHandler runs a single handler method for a given path id and user, with
// the chi route context and user-id context the real router would set. Mirrors
// the wiring in TestLogHandler_InvalidUUID_Integration.
func driveHandler(t *testing.T, h func(http.ResponseWriter, *http.Request), method, id, userID string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/logs/"+id, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := shared.WithUserID(req.Context(), userID)
	chiCtx := chi.NewRouteContext()
	chiCtx.URLParams.Add("id", id)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, chiCtx)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h(rr, req)
	return rr
}

// driveHandlerCrossUser runs a handler as a CROSS-USER request: the caller is
// `caller`, and `target` is stamped on the context exactly as the OPA
// authorisation middleware does after an allow (shared.WithTargetUser). The
// handler must then act on the target's rows, not the caller's.
func driveHandlerCrossUser(t *testing.T, h func(http.ResponseWriter, *http.Request), method, id, caller, target string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/logs/"+id, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := shared.WithUserID(req.Context(), caller)
	ctx = shared.WithTargetUser(ctx, target)
	chiCtx := chi.NewRouteContext()
	chiCtx.URLParams.Add("id", id)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, chiCtx)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h(rr, req)
	return rr
}

// TestLogHandler_CrossUser_Integration proves the AUTHORISED cross-user path
// against real Postgres: once OPA has allowed the (caller, target, action) tuple
// (simulated here by setting the target on the context), a privileged caller can
// read, update, AND delete ANOTHER user's log. Self-access is unchanged, and the
// cross-user list returns only the target's rows.
//
// The COMPLEMENTARY control — a plain (unauthorised) caller is stopped with a 403
// BEFORE the handler runs — lives in the OPA middleware and is proven by
// TestOPAAuth_CrossUserDeniedFailsClosed (fail-closed, no DB). The two layers are
// deliberately separate: OPA does function/attribute-level authorisation, the SQL
// here does object-level ownership (still scoped to the target's user_id).
func TestLogHandler_CrossUser_Integration(t *testing.T) {
	database := setupIntegrationDB(t)
	repo := NewLogRepository(database.Pool())
	svc := NewLogService(repo, database)
	handler := NewLogHandler(svc, &integrationLogger{}, &noopHandlerMetrics{})

	const (
		owner  = "a1000000-0000-4000-8000-00000000000a" // target: owns the rows
		caller = "b1000000-0000-4000-8000-00000000000b" // admin-attributed caller
	)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = database.Pool().Exec(ctx,
			"DELETE FROM logs WHERE user_id = ANY($1)", []string{owner, caller})
	})

	ownerRow, err := repo.createLog(ctx, owner, "2025-04-01T10:00:00Z", "owner's private row")
	if err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	// Cross-user READ: caller reads the owner's row (200, the owner's content).
	rr := driveHandlerCrossUser(t, handler.GetLog, http.MethodGet, ownerRow.ID, caller, owner, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("cross-user GET: got %d want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	var read Log
	if err := json.Unmarshal(rr.Body.Bytes(), &read); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if read.UserID != owner {
		t.Errorf("cross-user GET returned owner %q, want the target %q", read.UserID, owner)
	}

	// Cross-user UPDATE: caller edits the owner's row (200, persisted under owner).
	updateBody, _ := json.Marshal(UpdateRequest{Log: "edited cross-user"})
	rr = driveHandlerCrossUser(t, handler.UpdateLog, http.MethodPut, ownerRow.ID, caller, owner, updateBody)
	if rr.Code != http.StatusOK {
		t.Fatalf("cross-user PUT: got %d want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	persisted, err := repo.getLog(ctx, ownerRow.ID, owner)
	if err != nil {
		t.Fatalf("re-read owner row: %v", err)
	}
	if persisted.Log != "edited cross-user" {
		t.Errorf("cross-user update did not persist: got %q", persisted.Log)
	}

	// Cross-user LIST (offset) returns only the target owner's rows.
	list, err := svc.getLogsForTarget(ctx, owner, "1970-01-01T00:00:00Z", "2099-12-31T23:59:59Z", 100, 0)
	if err != nil {
		t.Fatalf("cross-user list: %v", err)
	}
	if len(list) != 1 || list[0].UserID != owner {
		t.Errorf("cross-user list must contain only the owner's rows, got %+v", list)
	}

	// Cross-user DELETE: caller deletes the owner's row (204, row gone).
	rr = driveHandlerCrossUser(t, handler.DeleteLog, http.MethodDelete, ownerRow.ID, caller, owner, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("cross-user DELETE: got %d want 204 (body: %s)", rr.Code, rr.Body.String())
	}
	if n := countLogsForUser(t, database, owner); n != 0 {
		t.Errorf("owner should have 0 rows after cross-user delete, got %d", n)
	}

	// Self-access is unchanged: caller acting on its OWN (absent) row is a clean
	// 404, never a leak, and never touches the owner's data.
	callerRow, err := repo.createLog(ctx, caller, "2025-04-02T10:00:00Z", "caller's own row")
	if err != nil {
		t.Fatalf("seed caller: %v", err)
	}
	rr = driveHandler(t, handler.GetLog, http.MethodGet, callerRow.ID, caller, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("self-access GET of own row: got %d want 200 (body: %s)", rr.Code, rr.Body.String())
	}
}

// TestLogRepository_CrossUserIsolation_Integration proves object-level
// authorization: user B can never reach user A's row by id or by listing.
// This is OWASP API Top 10 API1:2023 Broken Object Level Authorization (BOLA)
// and ASVS V8 (8.2.2 / 8.3.1 / 8.4.1). The control is the `AND user_id = $N`
// predicate in every repository query; removing it from ANY one query makes
// this test fail (the tripwire proof is recorded in the round report). Before
// this test, deleting that predicate passed the entire suite.
func TestLogRepository_CrossUserIsolation_Integration(t *testing.T) {
	database := setupIntegrationDB(t)
	repo := NewLogRepository(database.Pool())
	svc := NewLogService(repo, database)
	handler := NewLogHandler(svc, &integrationLogger{}, &noopHandlerMetrics{})

	const (
		userA = "a0000000-0000-4000-8000-00000000000a"
		userB = "b0000000-0000-4000-8000-00000000000b"
	)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = database.Pool().Exec(ctx,
			"DELETE FROM logs WHERE user_id = ANY($1)", []string{userA, userB})
	})

	// A owns one private row.
	aRow, err := repo.createLog(ctx, userA, "2025-01-15T10:00:00Z", "user A private row")
	if err != nil {
		t.Fatalf("seed A: %v", err)
	}
	// B owns its own rows, so B's list views are non-empty and must still exclude A's.
	for _, dt := range []string{"2025-02-01T10:00:00Z", "2025-02-01T11:00:00Z"} {
		if _, err := repo.createLog(ctx, userB, dt, "user B row"); err != nil {
			t.Fatalf("seed B: %v", err)
		}
	}

	// Enforcement point: B's direct repository read of A's row must fail.
	if _, err := repo.getLog(ctx, aRow.ID, userB); err == nil {
		t.Error("BOLA: repo.getLog let user B read user A's row")
	}

	// Handler layer: GET / PUT / DELETE of A's id as user B → 404 (never a 200 leak).
	updateBody, err := json.Marshal(UpdateRequest{Log: "hijacked by B"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, c := range []struct {
		name   string
		method string
		body   []byte
		h      func(http.ResponseWriter, *http.Request)
	}{
		{"GET", http.MethodGet, nil, handler.GetLog},
		{"PUT", http.MethodPut, updateBody, handler.UpdateLog},
		{"DELETE", http.MethodDelete, nil, handler.DeleteLog},
	} {
		t.Run(c.name+"_of_As_row_as_B_is_404", func(t *testing.T) {
			rr := driveHandler(t, c.h, c.method, aRow.ID, userB, c.body)
			if rr.Code != http.StatusNotFound {
				t.Errorf("%s A's row as B: got %d want 404 (body: %s)",
					c.method, rr.Code, rr.Body.String())
			}
		})
	}

	// Offset-mode list as B must not contain A's row.
	offset, err := repo.getLogs(ctx, userB,
		"1970-01-01T00:00:00Z", "2099-12-31T23:59:59Z", 100, 0)
	if err != nil {
		t.Fatalf("offset list B: %v", err)
	}
	for _, l := range offset {
		if l.ID == aRow.ID {
			t.Error("BOLA: offset list for user B contained user A's row")
		}
	}

	// Cursor-mode walk as B must not contain A's row (walkCursor also fails on dupes).
	for _, id := range walkCursor(t, svc, userB, 2, nil) {
		if id == aRow.ID {
			t.Error("BOLA: cursor walk for user B contained user A's row")
		}
	}

	// A's row still exists and is unchanged; B's PUT/DELETE did nothing.
	after, err := repo.getLog(ctx, aRow.ID, userA)
	if err != nil {
		t.Fatalf("A's row must still exist after B's attempts: %v", err)
	}
	if after.Log != "user A private row" {
		t.Errorf("A's row content changed to %q: a cross-user write leaked", after.Log)
	}
}

// TestLogRepository_InjectionContent_RoundTrips_Integration stores a classic
// SQL-injection payload as ordinary log content and proves it round-trips
// byte-for-byte while the table survives. Parameterised queries (ASVS 1.2.4)
// make the string data, never SQL.
//
// This is REGRESSION INSURANCE, not detection: it does not "catch" injection in
// the current code (there is none). It exists to fail loudly if a future
// refactor ever string-builds a query, at which point this payload would
// execute (dropping the table) instead of round-tripping.
func TestLogRepository_InjectionContent_RoundTrips_Integration(t *testing.T) {
	database := setupIntegrationDB(t)
	repo := NewLogRepository(database.Pool())

	const userID = "c0000000-0000-4000-8000-00000000000c"
	const payload = "'; DROP TABLE logs;--"
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = database.Pool().Exec(ctx, "DELETE FROM logs WHERE user_id = $1", userID)
	})

	created, err := repo.createLog(ctx, userID, "2025-01-15T10:00:00Z", payload)
	if err != nil {
		t.Fatalf("createLog with injection payload: %v", err)
	}
	if created.Log != payload {
		t.Errorf("stored content mutated: got %q want %q", created.Log, payload)
	}

	got, err := repo.getLog(ctx, created.ID, userID)
	if err != nil {
		t.Fatalf("getLog: %v", err)
	}
	if got.Log != payload {
		t.Errorf("round-trip mismatch: got %q want %q", got.Log, payload)
	}

	// If the payload had executed, `logs` would be gone and this follow-up query
	// would error. It succeeding proves the table survived.
	if _, err := repo.getLogs(ctx, userID,
		"1970-01-01T00:00:00Z", "2099-12-31T23:59:59Z", 10, 0); err != nil {
		t.Fatalf("follow-up query failed, logs table may be gone: %v", err)
	}
}
