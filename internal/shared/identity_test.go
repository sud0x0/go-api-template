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

// TestTargetUserFromContext mirrors the user-ID contract for the cross-user
// target: it canonicalises a valid UUID, rejects a non-UUID (so a malformed
// target can never reach a query), and reports ("", false) when absent (the
// self-access default the handler must fall back to).
func TestTargetUserFromContext(t *testing.T) {
	const canonical = "990e8400-e29b-41d4-a716-446655440009"

	t.Run("canonicalises a valid UUID", func(t *testing.T) {
		ctx := WithTargetUser(context.Background(), "990E8400-E29B-41D4-A716-446655440009")
		got, ok := TargetUserFromContext(ctx)
		if !ok || got != canonical {
			t.Errorf("got (%q, %v), want (%q, true)", got, ok, canonical)
		}
	})

	t.Run("rejects a non-UUID target", func(t *testing.T) {
		ctx := WithTargetUser(context.Background(), "not-a-uuid")
		if got, ok := TargetUserFromContext(ctx); ok {
			t.Errorf("non-UUID target should be rejected, got (%q, true)", got)
		}
	})

	t.Run("absent target is self-access", func(t *testing.T) {
		if got, ok := TargetUserFromContext(context.Background()); ok || got != "" {
			t.Errorf("missing: got (%q, %v), want (\"\", false)", got, ok)
		}
	})
}
