package userlog

import (
	"reflect"
	"strings"
	"testing"
)

// TestModelTagsDoNotHardcodeLengthLimit guards the single-source rule for the
// log length limit (Task 2). The maximum is owned by the service layer
// (shared.LogMaxChars); the request DTO tags must NOT carry a `rune_max` (or
// any numeric literal) that would duplicate the constant and shadow the
// service's structured LimitExceededError response.
//
// If someone re-adds `rune_max=10000` (or any rune_max) to a `log` field, this
// test fails — pairing with the final-verification grep that
// `internal/userlog/userlog_model.go` contains no "10000".
func TestModelTagsDoNotHardcodeLengthLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  reflect.Type
	}{
		{"CreateRequest", reflect.TypeFor[CreateRequest]()},
		{"UpdateRequest", reflect.TypeFor[UpdateRequest]()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			field, ok := tc.typ.FieldByName("Log")
			if !ok {
				t.Fatalf("%s has no Log field", tc.name)
			}
			tag := field.Tag.Get("validate")
			if strings.Contains(tag, "rune_max") {
				t.Errorf("%s.Log validate tag %q must not carry rune_max — the length "+
					"limit is owned by the service layer (shared.LogMaxChars)", tc.name, tag)
			}
			if strings.Contains(tag, "10000") {
				t.Errorf("%s.Log validate tag %q hardcodes the length limit as a literal; "+
					"use shared.LogMaxChars in the service instead", tc.name, tag)
			}
		})
	}
}
