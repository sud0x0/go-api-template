// Package httpclient is the canonical HTTP client for outbound calls.
//
// When the application calls an external service, use httpclient.Client
// instead of net/http.Client directly. It propagates the inbound request's
// X-Request-ID header into the outbound call automatically, so the same
// request ID appears in this service's logs AND the downstream service's
// logs. That makes distributed debugging tractable: grep one request ID
// across all services to reconstruct a single user request's path.
//
// When OpenTelemetry tracing is wired in, swap the underlying transport for
// otelhttp.NewTransport(httpClient.Transport) so W3C traceparent headers
// propagate automatically too.
//
// Template surface: this package has no in-repo production caller by design.
// it is the canonical outbound client for adopters' future features, and is
// exercised by httpclient_test.go. `make deadcode` therefore flags New, Do, and
// Get as unreachable from the two binaries' mains; that is expected, not dead
// code. Do not delete.
package httpclient

import (
	"context"
	"net/http"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// DefaultTimeout is a conservative default for outbound calls. Override with
// WithTimeout on the Client if a specific dependency needs longer.
const DefaultTimeout = 10 * time.Second

// Client wraps *http.Client and adds request-scoped header propagation.
// The zero value is not usable; construct via New.
type Client struct {
	http *http.Client
}

// New constructs a Client. Pass nil for an http.Client with DefaultTimeout
// and otherwise-default transport.
func New(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}
	return &Client{http: httpClient}
}

// Do issues req with request-scoped headers populated from ctx:
//
//   - X-Request-ID: propagated from chi's RequestID middleware on the
//     inbound request, so the downstream service logs the same ID.
//
// The returned response and error follow http.Client.Do semantics; callers
// must close the response body.
//
// Callers should pass the inbound request's ctx (or a derived ctx) so the
// request ID flows through. If ctx has no request ID (e.g. background work
// not bound to an HTTP request), no X-Request-ID header is added.
func (c *Client) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if reqID := chimw.GetReqID(ctx); reqID != "" {
		req.Header.Set("X-Request-ID", reqID)
	}
	// Callers control the URL; this package is a generic outbound client.
	// SSRF defence belongs in the caller (URL allow-listing for any user-
	// supplied URLs); this layer is correctly transport-only.
	//nolint:gosec // G704: URL provenance is the caller's responsibility
	return c.http.Do(req.WithContext(ctx))
}

// Get is a convenience wrapper for GET requests. Equivalent to
// http.NewRequestWithContext + Do.
func (c *Client) Get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(ctx, req)
}
