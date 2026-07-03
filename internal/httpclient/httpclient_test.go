package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// TestClient_PropagatesRequestID verifies the X-Request-ID propagation
// promise from package doc.
func TestClient_PropagatesRequestID(t *testing.T) {
	var seenRequestID string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenRequestID = r.Header.Get("X-Request-ID")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	const wantID = "abc-123"
	ctx := context.WithValue(context.Background(), chimw.RequestIDKey, wantID)

	c := New(nil)
	resp, err := c.Get(ctx, upstream.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_ = resp.Body.Close()

	if seenRequestID != wantID {
		t.Errorf("upstream did not see request ID — got %q, want %q", seenRequestID, wantID)
	}
}

// TestClient_OmitsHeaderWhenContextHasNoID verifies that background work
// without a request ID does not send a junk X-Request-ID header.
func TestClient_OmitsHeaderWhenContextHasNoID(t *testing.T) {
	var seenHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeader = r.Header.Get("X-Request-ID")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	c := New(nil)
	resp, err := c.Get(context.Background(), upstream.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_ = resp.Body.Close()

	if seenHeader != "" {
		t.Errorf("expected no X-Request-ID header, upstream saw %q", seenHeader)
	}
}

func TestClient_UsesProvidedHTTPClient(t *testing.T) {
	got := New(&http.Client{})
	if got.http == nil {
		t.Error("New(non-nil) should retain the provided client")
	}
}
