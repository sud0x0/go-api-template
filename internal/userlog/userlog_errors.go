package userlog

import (
	"errors"
	"fmt"

	"github.com/sud0x0/go-api-template/internal/shared"
	"github.com/sud0x0/go-api-template/internal/shared/logger"
)

// logLogger aliases the canonical logger interface from shared/logger.
type logLogger = logger.Logger

// ErrDatabase indicates a database operation failure.
var ErrDatabase = errors.New("database error")

// ErrUnauthorised indicates an unauthorised access attempt.
var ErrUnauthorised = errors.New("unauthorised access")

// ErrMissingParameters indicates required parameters are missing.
var ErrMissingParameters = errors.New("missing required parameters")

// ErrLogNotFound indicates a log entry was not found.
var ErrLogNotFound = errors.New("log not found")

// ErrInvalidInput indicates the input is invalid.
var ErrInvalidInput = errors.New("invalid input")

// ErrInvalidDateTime indicates an invalid date/time format.
var ErrInvalidDateTime = errors.New("invalid date_and_time: use RFC3339 e.g. 2006-01-02T15:04:05Z")

// InvalidDateTimeError carries a client-safe message naming WHICH input was
// malformed (optionally a batch index too). It Is(ErrInvalidDateTime) so existing
// errors.Is checks and the bounded error_type mapping (invalid_datetime) are
// unchanged.
//
// Field is one of "start_date", "end_date", "date_and_time" (the only three
// date inputs in this package) or "" when no specific field applies. These are
// compile-time constants at the call sites — a three-value set does not warrant
// enum machinery — so naming the field leaks nothing from a lower layer.
type InvalidDateTimeError struct {
	Field string // "start_date" | "end_date" | "date_and_time" | ""
	Index int    // batch entry index; -1 for non-batch paths
}

// Error composes the client-safe message from the ErrInvalidDateTime sentinel
// (the single source for the RFC3339 hint), in order: "entry N: " when Index >= 0,
// then "<field>: " when Field != "", then the sentinel text. Examples:
// "start_date: invalid date_and_time: use RFC3339 ...",
// "entry 2: date_and_time: invalid date_and_time: use RFC3339 ...". Nothing from
// lower layers leaks — every component is a constant or an integer index.
func (e *InvalidDateTimeError) Error() string {
	msg := ErrInvalidDateTime.Error()
	if e.Field != "" {
		msg = e.Field + ": " + msg
	}
	if e.Index >= 0 {
		msg = fmt.Sprintf("entry %d: %s", e.Index, msg)
	}
	return msg
}

// Is makes an InvalidDateTimeError match the ErrInvalidDateTime sentinel, so
// every existing errors.Is(err, ErrInvalidDateTime) keeps matching and the
// handleError mapping to error_type="invalid_datetime" is unchanged.
func (e *InvalidDateTimeError) Is(target error) bool {
	return target == ErrInvalidDateTime
}

// ErrValidation indicates a validation error.
var ErrValidation = errors.New("validation error")

// ErrInvalidReqBody indicates the request body is invalid.
var ErrInvalidReqBody = errors.New("invalid request body")

// ErrBodyTooLarge indicates the request body exceeded the server's size cap
// (chimw.RequestSize). Per RFC 9110 this maps to 413 Content Too Large, not
// 400 — see handleError.
var ErrBodyTooLarge = errors.New("request body too large")

// ErrInternalServer indicates an internal server error.
var ErrInternalServer = errors.New("internal server error")

// NewLogTooLongError creates a structured limit exceeded error.
func NewLogTooLongError(currentLen int) *shared.LimitExceededError {
	return shared.NewLimitExceededError(
		fmt.Sprintf("log exceeds maximum of %d characters", shared.LogMaxChars),
		shared.LogMaxChars,
		currentLen,
	)
}

// NewLogTooLongErrorAt is NewLogTooLongError with the failing batch index named
// in the message. The error TYPE stays bounded (limit_exceeded); only the
// human-readable message identifies which entry was over-limit.
func NewLogTooLongErrorAt(index, currentLen int) *shared.LimitExceededError {
	return shared.NewLimitExceededError(
		fmt.Sprintf("log at index %d exceeds maximum of %d characters", index, shared.LogMaxChars),
		shared.LogMaxChars,
		currentLen,
	)
}

// NewBatchTooLargeError creates a structured limit exceeded error for a batch
// over shared.MaxBatchSize. Bounded type limit_exceeded; limit/current carry the
// numbers so the client knows by how much they exceeded.
func NewBatchTooLargeError(current int) *shared.LimitExceededError {
	return shared.NewLimitExceededError(
		fmt.Sprintf("batch exceeds maximum of %d entries", shared.MaxBatchSize),
		shared.MaxBatchSize,
		current,
	)
}

// FeatureName is the constant `feature` label for api_errors_total emitted
// from this package. Bounded-cardinality: one value per Go package.
const FeatureName = "log"

// ErrType* are the bounded label values for the `error_type` label on
// api_errors_total. Adding a new sentinel error requires adding a
// corresponding ErrType constant and a case in handleError — never derive
// label values from err.Error() text.
const (
	ErrTypeLogNotFound       = "log_not_found"
	ErrTypeInvalidInput      = "invalid_input"
	ErrTypeMissingParameters = "missing_parameters"
	ErrTypeInvalidPagination = "invalid_pagination"
	ErrTypeInvalidDateTime   = "invalid_datetime"
	ErrTypeValidation        = "validation"
	ErrTypeInvalidReqBody    = "invalid_request_body"
	ErrTypeBodyTooLarge      = "body_too_large"
	ErrTypeUnauthorised      = "unauthorised"
	ErrTypeDatabase          = "database"
	ErrTypeLimitExceeded     = "limit_exceeded"
	ErrTypeInternal          = "internal"
)
