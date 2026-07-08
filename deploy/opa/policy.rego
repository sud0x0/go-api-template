# Starter ATTRIBUTE-BASED authorisation policy for the go-api-template resource
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
#   {"input": {"subject", "roles", "action", "resource", "method", "target_user"}}
# and enforces the boolean data.api.authz.allow, failing closed on anything else.
#
# ABAC, and how it can also be RBAC
# ---------------------------------
# Access is decided from ATTRIBUTES of the caller (subject, roles) and of the
# object (target_user), not from the route alone. A ROLE is just one attribute,
# so a rule that checks only roles IS role-based access control (RBAC) expressed
# in ABAC — see the admin rule below. A rule that checks a non-role attribute
# (here, team membership) is the scoped/attribute-based style — see the team-lead
# rule. Adopters choose the style by how they write Rego, not by swapping engines
# (NIST SP 800-162: a role is a valid ABAC attribute).
#
# THE NON-NEGOTIABLE RULE: every authorisation attribute (subject, roles, and any
# boundary attribute such as team) comes from the VALIDATED TOKEN or a TRUSTED
# store keyed by the token subject — NEVER from request input. The target_user
# MAY be request-supplied (it names WHICH object), but the caller's RIGHT to act
# on it is decided here, and the target's own attributes (its team) are looked up
# from trusted data, not taken from the request. (OWASP API1:2023 BOLA.)
#
# Written against INTERNAL role names ("admin", "user", "team-lead"), never IdP
# group names. The IdP-group to internal-role mapping happens in the app's
# authentication middleware (mapClaimsToRoles), so this policy stays IdP-agnostic
# and swappable.

package api.authz

import rego.v1

# DEFAULT DENY. Every request is denied unless a rule below explicitly allows it.
# This mirrors the app-side fail-closed default: no rule match means no access.
default allow := false

# The four fixed action verbs the app sends (methodToAction in the authz
# middleware). The policy reasons ONLY in these; it never invents an action the
# input cannot carry.
log_actions := {"read", "create", "update", "delete"}

# The request targets the log feature. resource is the chi ROUTE PATTERN
# (bounded), never the raw path.
is_logs if startswith(input.resource, "/api/v1/logs")

# --- SELF-ACCESS ---------------------------------------------------------------
# An authenticated subject may act on its OWN objects (target_user == subject).
# This is the ordinary case: no `?user=` parameter means target_user defaults to
# the caller's subject in the middleware. Row ownership is still enforced in SQL
# beneath this (WHERE user_id = $N), so this only gates "may the subject call
# this endpoint on its own data".
allow if {
	is_logs
	input.action in log_actions
	input.subject != ""
	input.target_user == input.subject
}

# --- RBAC-STYLE CROSS-USER (role-only) -----------------------------------------
# An admin may act on ANY target user for the log actions. This is RBAC: it
# checks only a role attribute. It is the whole basis for cross-user read AND
# write/delete by a privileged caller.
#
# The subject != "" guard is symmetric with the self-access rule: an
# unauthenticated request (empty subject) must never be allowed even if a roles
# claim leaks through, so every allow path fails closed on a missing subject.
allow if {
	is_logs
	input.action in log_actions
	input.subject != ""
	"admin" in input.roles
}

# --- ABAC / SCOPED CROSS-USER (attribute boundary) -----------------------------
# A team-lead may act on a target ONLY when the caller and the target are in the
# SAME team. This is the attribute-based example: the boundary is a non-role
# attribute (team), so authority is scoped, not global like admin's.
#
# If either the subject or the target has no known team, the user_team lookups
# below are undefined and this rule fails — a missing boundary attribute DENIES
# (fail-closed), it never defaults to allow.
allow if {
	is_logs
	input.action in log_actions
	input.subject != ""
	"team-lead" in input.roles
	user_team[input.subject] == user_team[input.target_user]
}

# user_team maps an internal user UUID to its team name.
#
# !!! LOUD WARNING !!! This hardcoded table exists ONLY to demonstrate the
# attribute-based (scoped) pattern, because the template ships no team data
# model. In a REAL deployment this boundary attribute MUST be fed from a TRUSTED
# source — a token claim mapped at the auth boundary, or a server-side store
# keyed by the token subject — and NEVER from request input. Reading a team from
# the request body or query would let a caller assert its own boundary and defeat
# the control (OWASP API1:2023 BOLA). Replace this table with `data.user_team`
# loaded from a signed bundle, or add a `team` claim to the OPA input mapped in
# the middleware.
user_team := {
	# "blue" team
	"1a000000-0000-4000-8000-0000000000a1": "blue",
	"1b000000-0000-4000-8000-0000000000b1": "blue",
	# "red" team
	"1c000000-0000-4000-8000-0000000000c1": "red",
}
