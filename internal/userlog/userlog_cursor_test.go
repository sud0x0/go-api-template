package userlog

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sud0x0/go-api-template/internal/shared"
)

// TestCursor_RoundTrip verifies encode→decode preserves the keyset components
// (nanosecond timestamp + canonical UUID).
func TestCursor_RoundTrip(t *testing.T) {
	ts := time.Date(2025, 1, 15, 11, 0, 0, 123456789, time.UTC)
	id := "660e8400-e29b-41d4-a716-446655440002"

	gotTS, gotID, err := decodeCursor(encodeCursor(ts, id))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !gotTS.Equal(ts) {
		t.Errorf("timestamp: got %v want %v", gotTS, ts)
	}
	if gotID != id {
		t.Errorf("id: got %q want %q", gotID, id)
	}
}

// TestDecodeCursor_Malformed verifies every malformed cursor variant is rejected
// as ErrInvalidPagination (→ 400) and never reaches SQL.
func TestDecodeCursor_Malformed(t *testing.T) {
	validUUID := "660e8400-e29b-41d4-a716-446655440002"
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

	cases := []struct {
		name   string
		cursor string
	}{
		{"not base64", "!!!not-base64!!!"},
		{"no separator", b64("2025-01-15T11:00:00Z" + validUUID)},
		{"bad timestamp", b64("not-a-time|" + validUUID)},
		{"bad uuid", b64("2025-01-15T11:00:00Z|not-a-uuid")},
		{"empty uuid", b64("2025-01-15T11:00:00Z|")},
		{"empty timestamp", b64("|" + validUUID)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := decodeCursor(c.cursor); err != shared.ErrInvalidPagination {
				t.Errorf("got %v want shared.ErrInvalidPagination", err)
			}
		})
	}
}

// TestListLogs_CursorOffsetMutualExclusion verifies supplying both ?cursor and
// ?offset is a 400 invalid_pagination — before any DB call.
func TestListLogs_CursorOffsetMutualExclusion(t *testing.T) {
	handler, mock, cv, cleanup := setupTestStack(t)
	defer cleanup()

	req := createRequest(http.MethodGet, "/api/v1/logs", nil, nil,
		map[string]string{"cursor": "", "offset": "1"})
	rr := httptest.NewRecorder()
	handler.ListLogs(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400. Body: %s", rr.Code, rr.Body.String())
	}
	assertErrorRecorded(t, cv, ErrTypeInvalidPagination, 1)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("mutual-exclusion reject must not touch the DB: %v", err)
	}
}

// TestListLogs_CursorMalformed verifies a malformed cursor token is a 400
// invalid_pagination and never reaches the keyset query.
func TestListLogs_CursorMalformed(t *testing.T) {
	handler, mock, cv, cleanup := setupTestStack(t)
	defer cleanup()

	req := createRequest(http.MethodGet, "/api/v1/logs", nil, nil,
		map[string]string{"cursor": "!!!garbage!!!"})
	rr := httptest.NewRecorder()
	handler.ListLogs(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400. Body: %s", rr.Code, rr.Body.String())
	}
	assertErrorRecorded(t, cv, ErrTypeInvalidPagination, 1)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("malformed cursor must not touch the DB: %v", err)
	}
}

// TestListLogs_CursorPageWalk walks two pages over mocked rows where two rows
// share the same date_and_time (a tie at the page boundary). It asserts the
// walk yields every row exactly once with no gaps or duplicates — proving the
// cursor carries the full keyset (date_and_time, id), not just the timestamp,
// and that the limit+1 "extra" row is re-fetched via the cursor rather than
// returned twice.
func TestListLogs_CursorPageWalk(t *testing.T) {
	handler, mock, _, cleanup := setupTestStack(t)
	defer cleanup()

	now := time.Now()
	const (
		idB = "660e8400-e29b-41d4-a716-446655440001"
		idY = "660e8400-e29b-41d4-a716-446655440002"
		idX = "660e8400-e29b-41d4-a716-446655440003"
		idA = "660e8400-e29b-41d4-a716-446655440004"
	)
	t2 := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	t1 := time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC) // idY and idX share this (tie)
	t0 := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	const start = "1970-01-01T00:00:00Z"
	const end = "2099-12-31T23:59:59Z"

	// Page 1 (first page): limit=2 → fetch 3. Return 3 rows; the service trims to
	// 2 and emits a cursor from the 2nd row (the tie row idY @ t1).
	keysetFirstRe := `SELECT .+ FROM logs WHERE user_id = \$1 AND date_and_time >= \$2 AND date_and_time <= \$3 ORDER BY date_and_time DESC, id DESC LIMIT \$4`
	mock.ExpectQuery(keysetFirstRe).
		WithArgs(testUserID, start, end, 3).
		WillReturnRows(mockLogRows(mock).
			AddRow(idB, testUserID, t2, "b", now, now).
			AddRow(idY, testUserID, t1, "y", now, now).
			AddRow(idX, testUserID, t1, "x", now, now))

	req1 := createRequest(http.MethodGet, "/api/v1/logs", nil, nil,
		map[string]string{"cursor": "", "limit": "2"})
	rr1 := httptest.NewRecorder()
	handler.ListLogs(rr1, req1)
	if rr1.Code != http.StatusOK {
		t.Fatalf("page1: got %d want 200. Body: %s", rr1.Code, rr1.Body.String())
	}
	var page1 CursorPage
	if err := json.Unmarshal(rr1.Body.Bytes(), &page1); err != nil {
		t.Fatalf("page1 decode: %v", err)
	}
	if len(page1.Logs) != 2 || page1.NextCursor == "" {
		t.Fatalf("page1: got %d logs, next=%q; want 2 logs and a non-empty cursor", len(page1.Logs), page1.NextCursor)
	}

	// The cursor must carry the FULL keyset of the last returned row (t1, idY).
	ct, cid, err := decodeCursor(page1.NextCursor)
	if err != nil {
		t.Fatalf("decode next cursor: %v", err)
	}
	if cid != idY || !ct.Equal(t1) {
		t.Fatalf("cursor keyset: got (%v,%s) want (%v,%s)", ct, cid, t1, idY)
	}

	// Page 2: resume after (t1, idY). The tie row idX @ t1 must come next (it is
	// NOT skipped despite sharing t1), then idA @ t0. Fewer than fetch → last page.
	keysetAfterRe := `SELECT .+ FROM logs WHERE user_id = \$1 AND date_and_time >= \$2 AND date_and_time <= \$3 AND \(date_and_time, id\) < \(\$4, \$5\) ORDER BY date_and_time DESC, id DESC LIMIT \$6`
	mock.ExpectQuery(keysetAfterRe).
		WithArgs(testUserID, start, end, ct, cid, 3).
		WillReturnRows(mockLogRows(mock).
			AddRow(idX, testUserID, t1, "x", now, now).
			AddRow(idA, testUserID, t0, "a", now, now))

	req2 := createRequest(http.MethodGet, "/api/v1/logs", nil, nil,
		map[string]string{"cursor": page1.NextCursor, "limit": "2"})
	rr2 := httptest.NewRecorder()
	handler.ListLogs(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("page2: got %d want 200. Body: %s", rr2.Code, rr2.Body.String())
	}
	var page2 CursorPage
	if err := json.Unmarshal(rr2.Body.Bytes(), &page2); err != nil {
		t.Fatalf("page2 decode: %v", err)
	}
	if page2.NextCursor != "" {
		t.Errorf("page2 should be the last page, got next_cursor=%q", page2.NextCursor)
	}

	// Union must be every row exactly once, in order, no gaps or duplicates.
	var gotIDs []string
	for _, l := range page1.Logs {
		gotIDs = append(gotIDs, l.ID)
	}
	for _, l := range page2.Logs {
		gotIDs = append(gotIDs, l.ID)
	}
	want := []string{idB, idY, idX, idA}
	if len(gotIDs) != len(want) {
		t.Fatalf("walk yielded %d rows want %d: %v", len(gotIDs), len(want), gotIDs)
	}
	seen := map[string]bool{}
	for i, id := range gotIDs {
		if seen[id] {
			t.Errorf("duplicate row %s across pages", id)
		}
		seen[id] = true
		if id != want[i] {
			t.Errorf("position %d: got %s want %s", i, id, want[i])
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %v", err)
	}
}
