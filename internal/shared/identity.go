package shared

import (
	"context"

	"github.com/google/uuid"
)

// userIDContextKey is the unexported context-key type for the authenticated
// user ID. A private struct type (not a bare string) guarantees no collision
// with context keys set by other packages or third-party libraries.
type userIDContextKey struct{}

// targetUserContextKey is the unexported context-key type for the TARGET user
// of a cross-user request: the user whose objects the caller is acting on when
// that user is NOT the caller. A private struct type (like userIDContextKey)
// avoids any cross-package collision.
type targetUserContextKey struct{}

// WithUserID returns a copy of ctx carrying the authenticated user ID.
//
// This is the auth contract's single seam: any auth middleware (which is
// infrastructure, not a feature) calls WithUserID after validating the
// request's credentials, without importing any feature package. Every
// repository query is then scoped by this ID (see the security rules).
//
// WithUserID is deliberately a PLAIN SETTER: it does not validate. The UUID
// contract is enforced at the single read point (UserIDFromContext), not here:
// one enforcement point cannot be bypassed by a second writer, whereas
// validating in two places reinvites drift. A middleware that sets a bad value
// is therefore caught uniformly as a 401 when the value is read, never as a 500
// at query time.
//
// Template surface: no in-repo production caller by design. Auth middleware is
// the adopter's to wire (see .claude/rules/decisions.md #13), and this is
// exercised by identity_test.go. `make deadcode` flags it as unreachable from
// the binaries' mains; that is expected, not dead code. Do not delete.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDContextKey{}, userID)
}

// UserIDFromContext returns the authenticated user ID in canonical UUID form and
// whether a valid one is present.
//
// This is the single enforcement point for the UUID user-ID contract. The
// template's `user_id` column is `UUID NOT NULL`, so a non-UUID subject must
// never reach a query: Postgres would raise a cast error, which the repository
// wraps as ErrDatabase and the handler reports as a 500 with
// error_type="database": a misconfigured auth integration misfiled as a
// database fault, polluting alerting. Enforcing the UUID shape here turns that
// into a clean 401 instead.
//
// Auth middleware that receives a non-UUID subject (an OAuth `sub` such as
// "auth0|12345", an email, or a numeric id) is responsible for mapping it to an
// internal UUID before calling WithUserID. If it does not, the request is
// rejected here as unauthenticated.
//
// Like parsePathUUID for path ids, this CANONICALISES rather than passing the
// raw value through: uuid.Parse accepts forms Postgres' uuid type rejects (the
// `urn:uuid:` prefix, `{...}` braces, uppercase), so the returned id is always
// the bare lowercase 8-4-4-4-12 form. Missing, empty, or non-UUID values return
// ("", false); handlers map ok=false to a 401.
func UserIDFromContext(ctx context.Context) (string, bool) {
	raw, ok := ctx.Value(userIDContextKey{}).(string)
	if !ok || raw == "" {
		return "", false
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return "", false
	}
	return id.String(), true
}

// WithTargetUser returns a copy of ctx carrying the canonical TARGET-user UUID
// for a cross-user request.
//
// It is the cross-user analogue of WithUserID and shares its discipline: it is a
// PLAIN SETTER (the UUID shape is enforced at the single read point,
// TargetUserFromContext). Only the OPA authorisation middleware sets it, and
// ONLY after OPA has explicitly authorised the (caller, target, action) tuple.
// Because the middleware runs before any feature handler and fails closed, a
// value set here always corresponds to an access the policy already allowed: a
// handler that reads it back can never reach a different user's rows on a path
// OPA did not approve. Cross-user access ALWAYS stays scoped to this target's
// user_id in the repository SQL (row-ownership, security.md rule 5); the target
// is DATA authorised by OPA, never authority derived from request input.
//
// Template surface: exercised by identity_test.go and the OPA middleware. Like
// WithUserID it has no in-repo production caller that `make deadcode` can see
// from a binary main, so it is flagged unreachable there by design. Do not
// delete.
func WithTargetUser(ctx context.Context, targetUserID string) context.Context {
	return context.WithValue(ctx, targetUserContextKey{}, targetUserID)
}

// TargetUserFromContext returns the canonical target-user UUID for a cross-user
// request and whether one is present. It is the single enforcement point for
// the target-user UUID contract, mirroring UserIDFromContext: uuid.Parse
// canonicalises the value and rejects a non-UUID (returning ok=false) so a
// malformed target can never reach a query as a cast-error 500.
//
// ok=false means "no cross-user target set" — the normal self-access case, in
// which the handler acts on the caller's OWN rows. A handler must therefore
// default to the caller's UUID when this returns false, never fail open.
func TargetUserFromContext(ctx context.Context) (string, bool) {
	raw, ok := ctx.Value(targetUserContextKey{}).(string)
	if !ok || raw == "" {
		return "", false
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return "", false
	}
	return id.String(), true
}
