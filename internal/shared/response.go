package shared

import (
	"bytes"
	"encoding/json"
	"net/http"
)

// ErrorResponse is the JSON error envelope that API handlers and the public
// router's 404/405 fallbacks return. (A few middleware-generated responses are
// body-less by design — chi's Timeout 504 and Recoverer panic-500.)
//
//	{"error": "<bounded_error_type>", "message": "<human-readable message>"}
//
// The `error` field is a bounded, machine-readable error type — it must come
// from a package-level constant (the feature packages' ErrType* constants),
// never from err.Error() text of a lower layer. `message` is a human-readable
// description safe to surface to clients.
//
// LimitExceededError is a superset of this shape: it serialises the same
// `error` and `message` fields and adds `limit` and `current`, so a client can
// always decode at least these two fields from any error response.
type ErrorResponse struct {
	ErrorType string `json:"error"`
	Message   string `json:"message"`
}

// Router-level bounded error types. These belong here (not in a feature
// package's ErrType* set) because they are emitted by the chi router itself —
// for unmatched paths and disallowed methods — before any feature handler runs.
// They carry NO feature label and are NOT recorded on api_errors_total
// (http_requests_total already counts them via the metrics middleware); they
// exist only so the router's responses use the same JSON envelope as everything
// else.
const (
	ErrTypeNotFound         = "not_found"
	ErrTypeMethodNotAllowed = "method_not_allowed"
	ErrTypeRateLimited      = "rate_limited"
)

// WriteJSONError writes the standard error envelope with the given status,
// Content-Type: application/json, and a bounded errorType + human message.
func WriteJSONError(w http.ResponseWriter, status int, errorType, message string) {
	WriteJSONErrorPayload(w, status, ErrorResponse{ErrorType: errorType, Message: message})
}

// WriteJSONErrorPayload writes an arbitrary error payload (ErrorResponse, or a
// superset such as *LimitExceededError) as a JSON body. The payload is encoded
// into a buffer FIRST so that a marshal failure can never leave a half-written
// response with the status already flushed (which would make a follow-up error
// write a silent no-op). Callers that need extra headers (e.g.
// WWW-Authenticate) must set them on w before calling.
//
// This is the single sanctioned error-writing helper: handlers, middleware,
// and health probes route every error body through here instead of net/http's
// http.Error (which writes text/plain and bypasses the envelope).
func WriteJSONErrorPayload(w http.ResponseWriter, status int, payload any) {
	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(payload); err != nil {
		// payload is a tiny struct of constants, so this is effectively
		// unreachable. Fall back to a fixed JSON envelope (still no http.Error,
		// still application/json) so the client always gets a well-formed body.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal","message":"error encoding response"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// nosemgrep: go.lang.security.audit.xss.no-direct-write-to-responsewriter.no-direct-write-to-responsewriter
	_, _ = w.Write(buf.Bytes())
}
