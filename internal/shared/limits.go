package shared

// LogMaxChars is the maximum number of runes (Unicode code points) allowed
// in a log entry. This matches PostgreSQL's VARCHAR(N) character counting.
// Adjust this constant as needed for your domain.
const LogMaxChars = 10000 // ~2,000 words, measured in runes not bytes

// MaxBatchSize is the maximum number of items accepted by a batch-create
// request. It bounds the size of a single atomic transaction (one round-trip
// per item inside the tx) so a client cannot hold a transaction (and its row
// locks) open arbitrarily long. The OpenAPI spec documents the same value
// (asserted by the contract test in internal/contract).
const MaxBatchSize = 100

// LimitExceededError represents a limit exceeded error with details.
// This is the canonical type used across all packages.
type LimitExceededError struct {
	ErrorType string `json:"error"`
	Message   string `json:"message"`
	Limit     int    `json:"limit"`
	Current   int    `json:"current"`
}

// Error implements the error interface.
func (e *LimitExceededError) Error() string {
	return e.Message
}

// NewLimitExceededError creates a new limit exceeded error.
func NewLimitExceededError(message string, limit, current int) *LimitExceededError {
	return &LimitExceededError{
		ErrorType: "limit_exceeded",
		Message:   message,
		Limit:     limit,
		Current:   current,
	}
}
