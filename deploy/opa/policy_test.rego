# Tests for the starter authorisation policy. Run with `opa test deploy/opa/`.
# Covers both allow and deny cases, including the default-deny fallthrough.

package api.authz

import rego.v1

# --- allow cases ---

test_admin_allowed_any_action if {
	allow with input as {
		"subject": "11111111-1111-1111-1111-111111111111",
		"roles": ["admin"],
		"action": "delete",
		"resource": "/api/v1/logs/{id}",
		"method": "DELETE",
	}
}

test_user_allowed_read_logs if {
	allow with input as {
		"subject": "22222222-2222-2222-2222-222222222222",
		"roles": ["user"],
		"action": "read",
		"resource": "/api/v1/logs",
		"method": "GET",
	}
}

test_user_allowed_create_logs if {
	allow with input as {
		"subject": "22222222-2222-2222-2222-222222222222",
		"roles": ["user"],
		"action": "create",
		"resource": "/api/v1/logs",
		"method": "POST",
	}
}

# --- deny cases ---

# A plain user may not delete (only admins may).
test_user_denied_delete if {
	not allow with input as {
		"subject": "22222222-2222-2222-2222-222222222222",
		"roles": ["user"],
		"action": "delete",
		"resource": "/api/v1/logs/{id}",
		"method": "DELETE",
	}
}

# No roles at all falls through to default deny.
test_no_roles_denied if {
	not allow with input as {
		"subject": "33333333-3333-3333-3333-333333333333",
		"roles": [],
		"action": "read",
		"resource": "/api/v1/logs",
		"method": "GET",
	}
}

# A user is scoped to the logs resource; an unrelated resource is denied.
test_user_denied_unknown_resource if {
	not allow with input as {
		"subject": "22222222-2222-2222-2222-222222222222",
		"roles": ["user"],
		"action": "read",
		"resource": "/api/v1/secrets",
		"method": "GET",
	}
}
