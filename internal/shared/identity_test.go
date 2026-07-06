package shared

import (
	"context"
	"testing"
)

// TestUserIDFromContext_CanonicalisesValidUUID verifies that a valid UUID in any
// uuid.Parse-accepted form comes back as the bare lowercase canonical UUID.
func TestUserIDFromContext_CanonicalisesValidUUID(t *testing.T) {
	const canonical = "550e8400-e29b-41d4-a716-446655440000"

	cases := []struct {
		name  string
		input string
	}{
		{"already canonical", canonical},
		{"uppercase", "550E8400-E29B-41D4-A716-446655440000"},
		{"urn prefix", "urn:uuid:550e8400-e29b-41d4-a716-446655440000"},
		{"brace wrapped", "{550e8400-e29b-41d4-a716-446655440000}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := WithUserID(context.Background(), c.input)
			got, ok := UserIDFromContext(ctx)
			if !ok {
				t.Fatalf("expected ok=true for valid UUID %q", c.input)
			}
			if got != canonical {
				t.Errorf("canonical form: got %q want %q", got, canonical)
			}
		})
	}
}

// TestUserIDFromContext_RejectsNonUUID verifies non-UUID subjects (the shapes an
// external IdP might hand through without mapping) are rejected. These must
// surface as 401 at the handler, never as a Postgres cast-error 500.
func TestUserIDFromContext_RejectsNonUUID(t *testing.T) {
	for _, bad := range []string{
		"auth0|12345",
		"user@example.com",
		"12345",
		"not-a-uuid",
		"550e8400-e29b-41d4-a716", // truncated
	} {
		t.Run(bad, func(t *testing.T) {
			ctx := WithUserID(context.Background(), bad)
			if got, ok := UserIDFromContext(ctx); ok {
				t.Errorf("non-UUID %q should be rejected, got (%q, true)", bad, got)
			}
		})
	}
}

// TestUserIDFromContext_EmptyAndMissing verifies empty and absent values both
// report ("", false).
func TestUserIDFromContext_EmptyAndMissing(t *testing.T) {
	t.Run("empty string", func(t *testing.T) {
		ctx := WithUserID(context.Background(), "")
		if got, ok := UserIDFromContext(ctx); ok || got != "" {
			t.Errorf("empty: got (%q, %v) want (\"\", false)", got, ok)
		}
	})
	t.Run("missing key", func(t *testing.T) {
		if got, ok := UserIDFromContext(context.Background()); ok || got != "" {
			t.Errorf("missing: got (%q, %v) want (\"\", false)", got, ok)
		}
	})
}
