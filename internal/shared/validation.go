// Package shared holds cross-feature concerns that must not live in any single
// feature package: input validation and the rune-length validators, the JSON
// error envelope, request limits, and the UUID user-ID identity boundary.
// Feature packages import shared; shared never imports a feature.
package shared

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-playground/validator/v10"
)

// ErrInvalidPagination indicates pagination parameters are invalid.
var ErrInvalidPagination = errors.New("invalid pagination parameters")

// Pagination limits.
//
// DefaultPageSize is the limit applied when the client omits ?limit. It is the
// single source of truth for that default: the OpenAPI spec documents the same
// value (asserted by the contract test in internal/contract) and the userlog
// service no longer carries a second copy.
const (
	DefaultPageSize = 100
	MaxPageSize     = 100
	MaxPageOffset   = 10000
)

// ValidatePagination validates and normalises pagination parameters.
// Returns (limit, offset, error).
func ValidatePagination(limitStr, offsetStr string) (int, int, error) {
	limit := DefaultPageSize
	offset := 0

	if limitStr != "" {
		l, err := strconv.Atoi(limitStr)
		if err != nil || l < 1 {
			return 0, 0, ErrInvalidPagination
		}
		if l > MaxPageSize {
			limit = MaxPageSize
		} else {
			limit = l
		}
	}

	if offsetStr != "" {
		o, err := strconv.Atoi(offsetStr)
		if err != nil || o < 0 {
			return 0, 0, ErrInvalidPagination
		}
		if o > MaxPageOffset {
			offset = MaxPageOffset
		} else {
			offset = o
		}
	}

	return limit, offset, nil
}

// SanitiseNullBytes removes null bytes from a string.
// Null bytes can cause issues with databases and should be stripped.
func SanitiseNullBytes(s string) string {
	return strings.ReplaceAll(s, "\x00", "")
}

// WriteUnauthorised writes a 401 Unauthorised response: it sets the
// WWW-Authenticate header and then emits the standard JSON error envelope via
// WriteJSONError. errorType must be a bounded constant (e.g. a feature's
// ErrType* value) so the `error` field never carries unbounded text.
func WriteUnauthorised(w http.ResponseWriter, errorType, message string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="api"`)
	WriteJSONError(w, http.StatusUnauthorized, errorType, message)
}

// RuneCountLen returns the number of Unicode code points in s.
//
// All user-facing length limits in this codebase are measured in runes
// (Unicode code points), matching PostgreSQL's VARCHAR(N) character
// counting.
//
// Grapheme clusters are deliberately NOT handled. A string like
// "👨‍👩‍👧" is one visible character (grapheme) but multiple runes
// (base emoji + zero-width joiners + modifier emoji). This function
// returns the rune count. The choice is intentional: grapheme counting
// requires golang.org/x/text/unicode/norm, adds a dependency for marginal
// benefit, and does not match the database's counting. Rune count is
// good enough for this domain. Do not change this to grapheme counting
// without a corresponding migration of every VARCHAR(N) column and a
// README update.
func RuneCountLen(s string) int {
	return utf8.RuneCountInString(s)
}

// RegisterRuneLenValidators adds three custom rules to a
// go-playground/validator instance:
//
//	rune_max — max number of runes allowed
//	rune_min — min number of runes required
//	rune_len — exact rune length
//
// All three operate on string fields. On a *string field, nil is
// treated as rune count 0 (so tie them with `omitempty` where null
// is acceptable).
//
// Usage (struct tags):
//
//	Name string `json:"name" validate:"required,rune_max=100"`
//	Bio  string `json:"bio"  validate:"omitempty,rune_max=500"`
func RegisterRuneLenValidators(v *validator.Validate) error {
	if err := v.RegisterValidation("rune_max", runeMaxValidator); err != nil {
		return err
	}
	if err := v.RegisterValidation("rune_min", runeMinValidator); err != nil {
		return err
	}
	if err := v.RegisterValidation("rune_len", runeLenValidator); err != nil {
		return err
	}
	return nil
}

func runeMaxValidator(fl validator.FieldLevel) bool {
	param := fl.Param()
	max, err := strconv.Atoi(param)
	if err != nil {
		return false
	}
	return RuneCountLen(fl.Field().String()) <= max
}

func runeMinValidator(fl validator.FieldLevel) bool {
	param := fl.Param()
	min, err := strconv.Atoi(param)
	if err != nil {
		return false
	}
	return RuneCountLen(fl.Field().String()) >= min
}

func runeLenValidator(fl validator.FieldLevel) bool {
	param := fl.Param()
	length, err := strconv.Atoi(param)
	if err != nil {
		return false
	}
	return RuneCountLen(fl.Field().String()) == length
}
