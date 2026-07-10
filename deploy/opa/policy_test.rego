# Tests for the starter ABAC authorisation policy. Run with `opa test deploy/opa/`.
# Covers self-access, RBAC-style cross-user (admin), scoped cross-user
# (team-lead within/outside boundary), unknown-role deny, missing-attribute deny,
# and the default-deny fallthrough.

package api.authz

import rego.v1

# Fixed test identities.
admin_id := "11111111-1111-1111-1111-111111111111"

user_id := "22222222-2222-2222-2222-222222222222"

lead_blue := "1a000000-0000-4000-8000-0000000000a1" # team-lead, team "blue"

target_blue := "1b000000-0000-4000-8000-0000000000b1" # another "blue" member

target_red := "1c000000-0000-4000-8000-0000000000c1" # "red" member

# --- self-access ---

test_self_access_read_allowed if {
	allow with input as {
		"subject": user_id,
		"roles": ["user"],
		"action": "read",
		"resource": "/api/v1/logs/{id}",
		"method": "GET",
		"target_user": user_id,
	}
}

# Self-access holds even for a no-role token: the subject may act on its own data
# (row ownership is still enforced in SQL beneath the policy).
test_self_access_no_roles_allowed if {
	allow with input as {
		"subject": user_id,
		"roles": [],
		"action": "update",
		"resource": "/api/v1/logs/{id}",
		"method": "PUT",
		"target_user": user_id,
	}
}

# --- RBAC-style cross-user (admin) ---

test_admin_cross_user_read_allowed if {
	allow with input as {
		"subject": admin_id,
		"roles": ["admin"],
		"action": "read",
		"resource": "/api/v1/logs/{id}",
		"method": "GET",
		"target_user": target_red,
	}
}

test_admin_cross_user_delete_allowed if {
	allow with input as {
		"subject": admin_id,
		"roles": ["admin"],
		"action": "delete",
		"resource": "/api/v1/logs/{id}",
		"method": "DELETE",
		"target_user": target_blue,
	}
}

test_admin_cross_user_update_allowed if {
	allow with input as {
		"subject": admin_id,
		"roles": ["admin"],
		"action": "update",
		"resource": "/api/v1/logs/{id}",
		"method": "PUT",
		"target_user": user_id,
	}
}

# --- scoped cross-user (team-lead) ---

# A team-lead acting on a target in the SAME team is allowed.
test_team_lead_within_boundary_allowed if {
	allow with input as {
		"subject": lead_blue,
		"roles": ["team-lead"],
		"action": "read",
		"resource": "/api/v1/logs/{id}",
		"method": "GET",
		"target_user": target_blue,
	}
}

# A team-lead acting on a target in a DIFFERENT team is denied.
test_team_lead_outside_boundary_denied if {
	not allow with input as {
		"subject": lead_blue,
		"roles": ["team-lead"],
		"action": "read",
		"resource": "/api/v1/logs/{id}",
		"method": "GET",
		"target_user": target_red,
	}
}

# A team-lead whose target has no known team (missing boundary attribute) is
# denied — the scoped rule fails closed, it never defaults to allow.
test_team_lead_missing_target_team_denied if {
	not allow with input as {
		"subject": lead_blue,
		"roles": ["team-lead"],
		"action": "read",
		"resource": "/api/v1/logs/{id}",
		"method": "GET",
		"target_user": "99999999-9999-4999-8999-999999999999",
	}
}

# --- deny cases ---

# An unknown role grants NO cross-user access (target != subject → default deny).
test_unknown_role_cross_user_denied if {
	not allow with input as {
		"subject": user_id,
		"roles": ["viewer"],
		"action": "read",
		"resource": "/api/v1/logs/{id}",
		"method": "GET",
		"target_user": target_blue,
	}
}

# A plain user may not act cross-user (only admin/team-lead can).
test_user_cross_user_denied if {
	not allow with input as {
		"subject": user_id,
		"roles": ["user"],
		"action": "delete",
		"resource": "/api/v1/logs/{id}",
		"method": "DELETE",
		"target_user": target_blue,
	}
}

# Missing subject (unauthenticated) is denied even for self-shaped input.
test_missing_subject_denied if {
	not allow with input as {
		"subject": "",
		"roles": [],
		"action": "read",
		"resource": "/api/v1/logs",
		"method": "GET",
		"target_user": "",
	}
}

# An admin role with an EMPTY subject is denied: the admin rule fails closed on a
# missing subject, symmetric with the self-access rule, so a leaked roles claim on
# an unauthenticated request can never grant cross-user access.
test_admin_empty_subject_denied if {
	not allow with input as {
		"subject": "",
		"roles": ["admin"],
		"action": "delete",
		"resource": "/api/v1/logs/{id}",
		"method": "DELETE",
		"target_user": target_blue,
	}
}

# Likewise a team-lead role with an empty subject is denied.
test_team_lead_empty_subject_denied if {
	not allow with input as {
		"subject": "",
		"roles": ["team-lead"],
		"action": "read",
		"resource": "/api/v1/logs/{id}",
		"method": "GET",
		"target_user": target_blue,
	}
}

# A recognised role on an unrelated resource is still denied.
test_unknown_resource_denied if {
	not allow with input as {
		"subject": admin_id,
		"roles": ["admin"],
		"action": "read",
		"resource": "/api/v1/secrets",
		"method": "GET",
		"target_user": admin_id,
	}
}

# --- anchored is_logs (tripwire for the unbounded-prefix fix) ---

# The exact collection path and the "/api/v1/logs/{id}" sub-path are logs routes.
test_is_logs_matches_collection if is_logs with input as {"resource": "/api/v1/logs"}

test_is_logs_matches_item if is_logs with input as {"resource": "/api/v1/logs/{id}"}

# An ADJACENT route that merely shares the "/api/v1/logs" prefix is NOT a logs
# route: a bare startswith would wrongly match these, so this is the tripwire.
test_is_logs_rejects_adjacent_settings if not is_logs with input as {"resource": "/api/v1/logsettings"}

test_is_logs_rejects_adjacent_admin if not is_logs with input as {"resource": "/api/v1/logs-admin"}

# Because is_logs is false for an adjacent route, even an admin is denied there
# (defence in depth: the sibling can't inherit the logs policy).
test_admin_denied_on_adjacent_route if {
	not allow with input as {
		"subject": admin_id,
		"roles": ["admin"],
		"action": "read",
		"resource": "/api/v1/logsettings",
		"method": "GET",
		"target_user": admin_id,
	}
}
