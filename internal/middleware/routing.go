package middleware

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// AuthorizedRoutes wires the two-layer auth model around a feature route set,
// getting both the middleware ORDER and the chi REGISTRATION KIND right so each
// layer actually works. Call it inside the r.Route("/api/v1", …) seam
// (see .claude/rules/decisions.md #13). A nil authn or authz is simply not
// registered, so the template still boots with either layer off.
//
// The two layers are registered DIFFERENTLY on purpose:
//
//   - authn (OIDC) is a PRE-ROUTING middleware (r.Use on the subrouter). It runs
//     before chi matches a leaf route, so an unauthenticated request is rejected
//     with 401 whether or not the path exists (no route-existence oracle for
//     anonymous callers). Authentication needs no route pattern.
//
//   - authz (OPA) is a POST-ROUTING, per-endpoint middleware, registered via an
//     inline Group. This is load-bearing: chi only fills in the LEAF route
//     pattern (e.g. "/api/v1/logs/{id}") AFTER a mux's Use-middleware chain runs,
//     inside routeHTTP → FindRoute. A plain r.Use(authz) on the subrouter would
//     therefore run BEFORE the leaf is resolved and hand OPA the MOUNT pattern
//     "/api/v1/*", so the policy's `input.resource` match ("/api/v1/logs…") never
//     fires and every request is denied 403 — the whole authz layer silently
//     non-functional. Registering authz on an inline Group bakes it into the
//     endpoint handler (chi's Mux.handle: inline muxes Chain the middleware onto
//     the route handler), which chi invokes only AFTER FindRoute has appended the
//     leaf pattern to the route context. OPA then receives the bounded leaf
//     pattern, exactly as the policy expects. See decisions.md #17/#21.
//
// Keeping this in one helper means every feature's routes get the correct wiring
// and a new feature cannot accidentally re-introduce the mount-pattern bug by
// hanging authz off the wrong mux.
func AuthorizedRoutes(r chi.Router, authn, authz func(http.Handler) http.Handler, register func(chi.Router)) {
	if authn != nil {
		r.Use(authn)
	}
	r.Group(func(r chi.Router) {
		if authz != nil {
			r.Use(authz)
		}
		register(r)
	})
}
