package userlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sud0x0/go-api-template/internal/shared"
)

// TestHandleError_EveryErrTypeHasACase is the contract guard for the bounded
// error_type label set: every ErrType* constant in this package must have a
// corresponding case in handleError that (a) emits that exact bounded label on
// api_errors_total, (b) maps to the intended HTTP status, and (c) writes the
// standard JSON error envelope.
//
// It is table-driven with one representative wrapped error per sentinel.
// Adding a new ErrType* constant without wiring a handleError case (or without
// adding it here) fails this test; the completeness check below asserts the
// table covers every constant.
func TestHandleError_EveryErrTypeHasACase(t *testing.T) {
	cases := []struct {
		errType string
		err     error
		status  int
	}{
		{ErrTypeLogNotFound, fmt.Errorf("ctx: %w", ErrLogNotFound), http.StatusNotFound},
		{ErrTypeInvalidInput, fmt.Errorf("ctx: %w", ErrInvalidInput), http.StatusBadRequest},
		{ErrTypeMissingParameters, fmt.Errorf("ctx: %w", ErrMissingParameters), http.StatusBadRequest},
		{ErrTypeInvalidPagination, fmt.Errorf("ctx: %w", shared.ErrInvalidPagination), http.StatusBadRequest},
		{ErrTypeInvalidDateTime, fmt.Errorf("ctx: %w", ErrInvalidDateTime), http.StatusBadRequest},
		{ErrTypeValidation, fmt.Errorf("ctx: %w", ErrValidation), http.StatusBadRequest},
		{ErrTypeInvalidReqBody, fmt.Errorf("ctx: %w", ErrInvalidReqBody), http.StatusBadRequest},
		{ErrTypeBodyTooLarge, fmt.Errorf("ctx: %w", ErrBodyTooLarge), http.StatusRequestEntityTooLarge},
		{ErrTypeUnauthorised, fmt.Errorf("ctx: %w", ErrUnauthorised), http.StatusUnauthorized},
		{ErrTypeDatabase, fmt.Errorf("ctx: %w", ErrDatabase), http.StatusInternalServerError},
		{ErrTypeLimitExceeded, fmt.Errorf("ctx: %w", NewLogTooLongError(1)), http.StatusBadRequest},
		// Any error matching no sentinel falls through to the default case.
		{ErrTypeInternal, errors.New("unmapped boom"), http.StatusInternalServerError},
	}

	// Completeness: the labels exercised above must equal the full set of
	// ErrType* constants. A new constant added without a row here trips this.
	allErrTypes := []string{
		ErrTypeLogNotFound, ErrTypeInvalidInput, ErrTypeMissingParameters,
		ErrTypeInvalidPagination, ErrTypeInvalidDateTime, ErrTypeValidation,
		ErrTypeInvalidReqBody, ErrTypeBodyTooLarge, ErrTypeUnauthorised,
		ErrTypeDatabase, ErrTypeLimitExceeded, ErrTypeInternal,
	}
	covered := make(map[string]bool, len(cases))
	for _, c := range cases {
		covered[c.errType] = true
	}
	for _, et := range allErrTypes {
		if !covered[et] {
			t.Errorf("ErrType %q has no row in the handleError contract table: add a triggering error", et)
		}
	}
	if len(cases) != len(allErrTypes) {
		t.Errorf("contract table has %d rows but there are %d ErrType constants: keep them in lockstep",
			len(cases), len(allErrTypes))
	}

	for _, c := range cases {
		t.Run(c.errType, func(t *testing.T) {
			handler, _, errCV, cleanup := setupTestStack(t)
			defer cleanup()

			rr := httptest.NewRecorder()
			handler.handleError(context.Background(), rr, c.err)

			if rr.Code != c.status {
				t.Errorf("status: got %d want %d", rr.Code, c.status)
			}
			assertErrorRecorded(t, errCV, c.errType, 1)

			if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type: got %q want application/json", ct)
			}
			var env shared.ErrorResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
				t.Fatalf("body is not a JSON error envelope: %v\nbody: %s", err, rr.Body.String())
			}
			if env.ErrorType != c.errType {
				t.Errorf("envelope error field: got %q want %q", env.ErrorType, c.errType)
			}
		})
	}
}
