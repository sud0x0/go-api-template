// Package userlog is the reference feature package: the canonical
// handler → service → repository slice to copy for every new feature. It is
// named userlog, not log, to avoid shadowing the standard library's log
// package.
package userlog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"

	"github.com/sud0x0/go-api-template/internal/shared"
	"github.com/sud0x0/go-api-template/internal/shared/logger"
)

// HandlerMetrics is the subset of metrics the handler emits. The metrics
// package's *Metrics implements this; tests can provide stubs backed by a
// private CounterVec.
//
// The authenticated user ID is read from the request context via
// shared.UserIDFromContext — auth middleware sets it with shared.WithUserID.
// The key lives in internal/shared (not this feature package) so middleware,
// which is infrastructure, never has to import a feature.
type HandlerMetrics interface {
	IncAPIError(feature, errorType string)
}

// Handler handles HTTP requests for logs.
type Handler struct {
	service  logService
	logger   logLogger
	validate *validator.Validate
	metrics  HandlerMetrics
}

// NewLogHandler creates a new Handler. validate is initialised here so the
// handler is self-contained and testable. metrics is required — pass a
// real *metrics.Metrics in production or a stub in tests.
func NewLogHandler(service logService, log logLogger, m HandlerMetrics) *Handler {
	v := validator.New()
	if err := shared.RegisterRuneLenValidators(v); err != nil {
		panic("log_handler: failed to register rune validators: " + err.Error())
	}
	return &Handler{
		service:  service,
		logger:   log,
		validate: v,
		metrics:  m,
	}
}

// Routes registers the log feature's five endpoints on r. Mount it inside the
// authenticated `/api/v1` group in cmd/api/main.go:
//
//	r.Route("/api/v1", func(r chi.Router) {
//	    // r.Use(authMiddleware.Handler)
//	    logHandler.Routes(r)
//	})
//
// Keeping route registration next to the handlers means adding a feature wires
// exactly one line in main.go (its Routes call), not five scattered r.Get/Post
// lines — and the feature owns its own URL shape.
func (h *Handler) Routes(r chi.Router) {
	r.Get("/logs", h.ListLogs)
	r.Post("/logs", h.CreateLog)
	// Static /logs/batch is registered alongside the /logs/{id} wildcard; chi
	// matches the static segment first, so a POST to /logs/batch never falls
	// through to the {id} routes.
	r.Post("/logs/batch", h.BatchCreate)
	r.Get("/logs/{id}", h.GetLog)
	r.Put("/logs/{id}", h.UpdateLog)
	r.Delete("/logs/{id}", h.DeleteLog)
}

// parsePathUUID validates that a path parameter is a well-formed UUID and
// returns its canonical string form (8-4-4-4-12, lowercase, no decoration).
//
// uuid.Parse is lenient: it accepts forms Postgres' uuid type rejects, notably
// the `urn:uuid:` prefix and `{...}` braces. Passing the raw string through
// would turn a malformed-input 400 into a database-error 500. Returning
// id.String() canonicalises the value so the repository always sends Postgres a
// form it accepts. A truly malformed id returns ErrInvalidInput (→ 400) and
// never reaches the database.
func parsePathUUID(raw string) (string, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return "", ErrInvalidInput
	}
	return id.String(), nil
}

// decodeJSONBody strictly decodes exactly one JSON object from r.Body into dst.
//
// It enforces three rules and maps each failure to a bounded sentinel:
//   - Body over the server's size cap (chimw.RequestSize wraps the body in a
//     http.MaxBytesReader) → ErrBodyTooLarge, which handleError maps to 413
//     (RFC 9110 §15.5.14), not a generic 400.
//   - Unknown fields (DisallowUnknownFields) → ErrInvalidReqBody (400).
//   - Trailing data after the first object (a second JSON value, or extra
//     tokens) → ErrInvalidReqBody (400). A well-formed single object leaves the
//     decoder at io.EOF.
func decodeJSONBody(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return ErrBodyTooLarge
		}
		return ErrInvalidReqBody
	}

	// A second decode must hit EOF — anything else means the client sent more
	// than one JSON value (e.g. two concatenated objects) or trailing garbage.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalidReqBody
	}
	return nil
}

// handleError maps package errors to HTTP responses and increments the
// api_errors_total counter with a bounded error_type label. This is the
// single place where errors are logged AND counted — lower layers wrap
// and return.
func (h *Handler) handleError(ctx context.Context, w http.ResponseWriter, err error) {
	log := logger.FromContext(ctx, h.logger)

	// Structured limit errors carry their own superset envelope (error +
	// message + limit + current). It is JSON-compatible with ErrorResponse, so
	// it goes straight through the shared writer.
	var limitErr *shared.LimitExceededError
	if errors.As(err, &limitErr) {
		h.metrics.IncAPIError(FeatureName, ErrTypeLimitExceeded)
		shared.WriteJSONErrorPayload(w, http.StatusBadRequest, limitErr)
		return
	}

	switch {
	case errors.Is(err, ErrDatabase):
		h.metrics.IncAPIError(FeatureName, ErrTypeDatabase)
		// Log the underlying cause — multi-%w wrapping preserves it for diagnostics.
		log.LogError(ErrDatabase, err)
		shared.WriteJSONError(w, http.StatusInternalServerError, ErrTypeDatabase, "database error occurred")
	case errors.Is(err, ErrUnauthorised):
		h.metrics.IncAPIError(FeatureName, ErrTypeUnauthorised)
		shared.WriteUnauthorised(w, ErrTypeUnauthorised, "unauthorised access")
	case errors.Is(err, ErrMissingParameters):
		h.metrics.IncAPIError(FeatureName, ErrTypeMissingParameters)
		shared.WriteJSONError(w, http.StatusBadRequest, ErrTypeMissingParameters, "missing required parameters")
	case errors.Is(err, ErrInvalidInput):
		h.metrics.IncAPIError(FeatureName, ErrTypeInvalidInput)
		shared.WriteJSONError(w, http.StatusBadRequest, ErrTypeInvalidInput, "invalid input")
	case errors.Is(err, shared.ErrInvalidPagination):
		h.metrics.IncAPIError(FeatureName, ErrTypeInvalidPagination)
		shared.WriteJSONError(w, http.StatusBadRequest, ErrTypeInvalidPagination, "invalid pagination parameters")
	case errors.Is(err, ErrLogNotFound):
		h.metrics.IncAPIError(FeatureName, ErrTypeLogNotFound)
		shared.WriteJSONError(w, http.StatusNotFound, ErrTypeLogNotFound, "log not found")
	case errors.Is(err, ErrInvalidDateTime):
		h.metrics.IncAPIError(FeatureName, ErrTypeInvalidDateTime)
		// Derive the 400 message from the sentinel itself so the RFC3339 hint
		// string is written in exactly one place (the ErrInvalidDateTime var).
		// A structured InvalidDateTimeError (batch path) additionally names the
		// failing entry index; its Error() is composed from the same sentinel
		// text, so nothing from lower layers leaks. The bounded type is
		// unchanged (invalid_datetime).
		msg := ErrInvalidDateTime.Error()
		var dtErr *InvalidDateTimeError
		if errors.As(err, &dtErr) {
			msg = dtErr.Error()
		}
		shared.WriteJSONError(w, http.StatusBadRequest, ErrTypeInvalidDateTime, msg)
	case errors.Is(err, ErrBodyTooLarge):
		h.metrics.IncAPIError(FeatureName, ErrTypeBodyTooLarge)
		shared.WriteJSONError(w, http.StatusRequestEntityTooLarge, ErrTypeBodyTooLarge, "request body exceeds the maximum allowed size")
	case errors.Is(err, ErrValidation):
		h.metrics.IncAPIError(FeatureName, ErrTypeValidation)
		shared.WriteJSONError(w, http.StatusBadRequest, ErrTypeValidation, "validation error")
	case errors.Is(err, ErrInvalidReqBody):
		h.metrics.IncAPIError(FeatureName, ErrTypeInvalidReqBody)
		shared.WriteJSONError(w, http.StatusBadRequest, ErrTypeInvalidReqBody, "invalid request body")
	default:
		h.metrics.IncAPIError(FeatureName, ErrTypeInternal)
		log.LogError(ErrInternalServer, err)
		shared.WriteJSONError(w, http.StatusInternalServerError, ErrTypeInternal, "internal server error")
	}
}

// responseJSON marshals data to a buffer before writing the header and body.
// This prevents the partial-response bug where WriteHeader is sent before
// a marshal error is discovered, making a subsequent http.Error a no-op.
func (h *Handler) responseJSON(ctx context.Context, w http.ResponseWriter, data any, status int) {
	log := logger.FromContext(ctx, h.logger)
	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(data); err != nil {
		log.LogError(ErrInternalServer, err)
		shared.WriteJSONError(w, http.StatusInternalServerError, ErrTypeInternal, "error encoding response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// nosemgrep: go.lang.security.audit.xss.no-direct-write-to-responsewriter.no-direct-write-to-responsewriter
	_, _ = w.Write(buf.Bytes())
}

// GetLog handles GET /logs/{id}.
func (h *Handler) GetLog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := shared.UserIDFromContext(ctx)
	if !ok {
		h.handleError(ctx, w, ErrUnauthorised)
		return
	}

	id, err := parsePathUUID(shared.SanitiseNullBytes(chi.URLParam(r, "id")))
	if err != nil {
		h.handleError(ctx, w, err)
		return
	}

	entry, err := h.service.getLog(ctx, id, userID)
	if err != nil {
		h.handleError(ctx, w, err)
		return
	}

	h.responseJSON(ctx, w, entry, http.StatusOK)
}

// ListLogs handles GET /logs in two mutually-exclusive pagination modes:
//
//   - Offset mode (default, backwards-compatible): ?limit&offset → a BARE array
//     [...]. Offset is capped at shared.MaxPageOffset — a hard reachability
//     limit; deeper data is only reachable via cursor mode.
//   - Cursor (keyset) mode: ?cursor[=<token>] → a wrapped object
//     {"logs":[...],"next_cursor":"..."}. The first page uses an empty cursor
//     (?cursor=); each response carries the next_cursor to fetch the next page
//     (omitted on the last page). Recommended for deep or concurrently-mutated
//     lists — it does not walk-and-discard rows and pages don't shift under
//     inserts.
//
// Supplying both ?cursor and ?offset is a 400 (invalid_pagination). When
// start_date or end_date are omitted the service defaults to 1970-01-01 and
// 2099-12-31 respectively — always supply a date range in production.
func (h *Handler) ListLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := shared.UserIDFromContext(ctx)
	if !ok {
		h.handleError(ctx, w, ErrUnauthorised)
		return
	}

	q := r.URL.Query()
	limitStr := shared.SanitiseNullBytes(q.Get("limit"))
	startDate := shared.SanitiseNullBytes(q.Get("start_date"))
	endDate := shared.SanitiseNullBytes(q.Get("end_date"))

	// Cursor mode is opt-in by the PRESENCE of ?cursor (empty value = first
	// page). It is mutually exclusive with ?offset.
	if q.Has("cursor") {
		if q.Has("offset") {
			h.handleError(ctx, w, shared.ErrInvalidPagination)
			return
		}
		// Reuse ValidatePagination for the limit only (offset unused in cursor
		// mode); it enforces limit >= 1 and the MaxPageSize cap.
		limit, _, err := shared.ValidatePagination(limitStr, "")
		if err != nil {
			h.handleError(ctx, w, err)
			return
		}
		cursor := shared.SanitiseNullBytes(q.Get("cursor"))
		logs, nextCursor, err := h.service.getLogsCursor(ctx, userID, startDate, endDate, cursor, limit)
		if err != nil {
			h.handleError(ctx, w, err)
			return
		}
		h.responseJSON(ctx, w, CursorPage{Logs: logs, NextCursor: nextCursor}, http.StatusOK)
		return
	}

	// Offset mode (default) — bare array, unchanged shape.
	offsetStr := shared.SanitiseNullBytes(q.Get("offset"))
	limit, offset, err := shared.ValidatePagination(limitStr, offsetStr)
	if err != nil {
		h.handleError(ctx, w, err)
		return
	}

	logs, err := h.service.getLogs(ctx, userID, startDate, endDate, limit, offset)
	if err != nil {
		h.handleError(ctx, w, err)
		return
	}

	h.responseJSON(ctx, w, logs, http.StatusOK)
}

// CreateLog handles POST /logs.
func (h *Handler) CreateLog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := shared.UserIDFromContext(ctx)
	if !ok {
		h.handleError(ctx, w, ErrUnauthorised)
		return
	}

	var req CreateRequest
	if err := decodeJSONBody(r, &req); err != nil {
		h.handleError(ctx, w, err)
		return
	}

	req.DateAndTime = shared.SanitiseNullBytes(req.DateAndTime)
	req.Log = shared.SanitiseNullBytes(req.Log)

	if err := h.validate.Struct(req); err != nil {
		h.handleError(ctx, w, ErrValidation)
		return
	}

	entry, err := h.service.createLog(ctx, userID, req)
	if err != nil {
		h.handleError(ctx, w, err)
		return
	}

	h.responseJSON(ctx, w, entry, http.StatusCreated)
}

// BatchCreate handles POST /logs/batch. It creates every entry atomically — any
// single failure rolls back the whole batch — and returns 201 with the created
// entries in request order. The atomic transaction is owned by the service (see
// createLogs); this handler only validates and dispatches.
func (h *Handler) BatchCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := shared.UserIDFromContext(ctx)
	if !ok {
		h.handleError(ctx, w, ErrUnauthorised)
		return
	}

	var req BatchCreateRequest
	if err := decodeJSONBody(r, &req); err != nil {
		h.handleError(ctx, w, err)
		return
	}

	// Bound the batch size BEFORE struct validation so an over-size batch returns
	// the structured limit_exceeded envelope (with limit/current), not a generic
	// validation 400.
	if len(req.Logs) > shared.MaxBatchSize {
		h.handleError(ctx, w, NewBatchTooLargeError(len(req.Logs)))
		return
	}

	// Strip null bytes on every entry (Postgres TEXT cannot store \x00), exactly
	// as the single-create path does.
	for i := range req.Logs {
		req.Logs[i].DateAndTime = shared.SanitiseNullBytes(req.Logs[i].DateAndTime)
		req.Logs[i].Log = shared.SanitiseNullBytes(req.Logs[i].Log)
	}

	// `min=1` rejects an empty/absent list (400) — `required` alone does NOT (a
	// non-nil empty slice from `"logs":[]` is not the zero value); see the
	// BatchCreateRequest model comment. `dive` validates each entry's required
	// fields exactly as a single create does. The rune limit and RFC3339 date
	// are enforced per entry in the service.
	if err := h.validate.Struct(req); err != nil {
		h.handleError(ctx, w, ErrValidation)
		return
	}

	entries, err := h.service.createLogs(ctx, userID, req.Logs)
	if err != nil {
		h.handleError(ctx, w, err)
		return
	}

	h.responseJSON(ctx, w, entries, http.StatusCreated)
}

// UpdateLog handles PUT /logs/{id}.
// Only the log content is mutable; the timestamp is fixed at creation.
func (h *Handler) UpdateLog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := shared.UserIDFromContext(ctx)
	if !ok {
		h.handleError(ctx, w, ErrUnauthorised)
		return
	}

	id, err := parsePathUUID(shared.SanitiseNullBytes(chi.URLParam(r, "id")))
	if err != nil {
		h.handleError(ctx, w, err)
		return
	}

	var req UpdateRequest
	if err := decodeJSONBody(r, &req); err != nil {
		h.handleError(ctx, w, err)
		return
	}

	req.Log = shared.SanitiseNullBytes(req.Log)

	if err := h.validate.Struct(req); err != nil {
		h.handleError(ctx, w, ErrValidation)
		return
	}

	entry, err := h.service.updateLog(ctx, id, userID, req)
	if err != nil {
		h.handleError(ctx, w, err)
		return
	}

	h.responseJSON(ctx, w, entry, http.StatusOK)
}

// DeleteLog handles DELETE /logs/{id}.
func (h *Handler) DeleteLog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := shared.UserIDFromContext(ctx)
	if !ok {
		h.handleError(ctx, w, ErrUnauthorised)
		return
	}

	id, err := parsePathUUID(shared.SanitiseNullBytes(chi.URLParam(r, "id")))
	if err != nil {
		h.handleError(ctx, w, err)
		return
	}

	if err := h.service.deleteLog(ctx, id, userID); err != nil {
		h.handleError(ctx, w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
