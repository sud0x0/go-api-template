package userlog

import "time"

// Log represents a log entry.
type Log struct {
	ID          string    `json:"id"`
	UserID      string    `json:"user_id"`
	DateAndTime time.Time `json:"date_and_time"`
	Log         string    `json:"log"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Validate checks that data retrieved from the database meets expected constraints.
// This is defence-in-depth: even trusted database values are validated before use.
func (l *Log) Validate() error {
	if l.ID == "" {
		return ErrInvalidInput
	}
	if l.UserID == "" {
		return ErrInvalidInput
	}
	if l.DateAndTime.IsZero() {
		return ErrInvalidInput
	}
	if l.CreatedAt.IsZero() {
		return ErrInvalidInput
	}
	if l.UpdatedAt.IsZero() {
		return ErrInvalidInput
	}
	return nil
}

// CreateRequest is the create payload (POST /logs).
//
// The `log` field carries only `required` here, NOT a `rune_max` tag. The
// maximum length is enforced once, in the service layer, against
// shared.LogMaxChars (the single source of truth). Putting a second numeric
// limit in this struct tag would (a) duplicate the constant as a literal and
// (b) shadow the service check, so the structured LimitExceededError body,
// which tells the client the limit and their current count, would never
// reach them (the generic validation 400 would fire first). See
// userlog_service.createLog and the reflection guard in userlog_model_test.go.
type CreateRequest struct {
	DateAndTime string `json:"date_and_time" validate:"required"`
	Log         string `json:"log"           validate:"required"`
}

// UpdateRequest is the update payload (PUT /logs/{id}).
// DateAndTime is fixed at creation time and cannot be modified.
//
// As with CreateRequest, the length limit is owned by the service layer, not a
// struct tag. See the comment there.
type UpdateRequest struct {
	Log string `json:"log" validate:"required"`
}

// CursorPage is the response shape for cursor-mode list requests:
// {"logs": [...], "next_cursor": "..."}. next_cursor is OMITTED on the last
// page (no more rows). Offset-mode requests keep the bare `[...]` array for
// backwards compatibility; only cursor mode uses this wrapper. The asymmetry
// is documented in the OpenAPI spec.
type CursorPage struct {
	Logs       []Log  `json:"logs"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// BatchCreateRequest is the payload for POST /logs/batch: a list of entries
// created atomically (all-or-nothing). `dive` validates each element exactly as
// a single CreateRequest is validated (required fields); the element COUNT is
// bounded separately against shared.MaxBatchSize in the handler so an over-size
// batch returns the structured LimitExceededError rather than a generic
// validation error. `min=1` rejects an empty/absent list as a 400; note that
// `required` alone does NOT (a non-nil empty slice from `"logs":[]` is not the
// zero value), which is why min=1 is required here. `min` is the correct tag for
// a slice COUNT; the rune_* tags are only for string rune-vs-byte length.
type BatchCreateRequest struct {
	Logs []CreateRequest `json:"logs" validate:"required,min=1,dive"`
}
