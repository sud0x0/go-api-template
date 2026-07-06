# Starter FUNCTION-LEVEL authorisation policy for the go-api-template resource
# server (LAYER 2 of the two-layer auth model).
#
# LOCAL-DEV POLICY SOURCE: this file is volume-mounted read-only into the OPA
# sidecar by compose.dev.yaml. In PRODUCTION, do not bind-mount policy — serve it
# as a signed policy BUNDLE from a remote bundle server so policy is versioned,
# signed, and distributed independently of the app (OPA guidance).
#
# The package and rule path MUST match OPA_DECISION_PATH (v1/data/api/authz/allow):
# the REST Data API path v1/data/api/authz/allow resolves to
# data.api.authz.allow, i.e. package api.authz + rule allow. The app POSTs
#   {"input": {"subject", "roles", "action", "resource", "method"}}
# and enforces the boolean data.api.authz.allow, failing closed on anything else.
#
# Written against INTERNAL role names ("admin", "user"), never IdP group names.
# The IdP-group to internal-role mapping happens in the app's authentication
# middleware (mapClaimsToRoles), so this policy stays IdP-agnostic and swappable.

package api.authz

import rego.v1

# DEFAULT DENY. Every request is denied unless a rule below explicitly allows it.
# This mirrors the app-side fail-closed default: no rule match means no access.
default allow := false

# An admin may perform any action on any resource.
allow if "admin" in input.roles

# A user may read and create logs. Row ownership (which specific rows this user
# may see) is NOT decided here — it stays in the repository's WHERE user_id = $N
# SQL. This rule is only the function-level gate: "may a user call this endpoint".
allow if {
	"user" in input.roles
	input.action in {"read", "create"}
	startswith(input.resource, "/api/v1/logs")
}
