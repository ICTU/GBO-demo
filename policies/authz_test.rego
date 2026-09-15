package authz_test

import data.authz

# The metadata path is gated by subject, method and endpoint. No declared
# property takes part, so the fixture carries none — every case below turns
# on one of the three conditions that remain.
metadata_input(method, path) := {
	"subject": {"id": "99999999900000000100", "type": "identity"},
	"action": {"id": method, "type": "name"},
	"resource": {"id": path, "type": "uri"},
	"context": {},
}

metadata_input_for_actor(actor) := object.union(
	metadata_input("GET", "/.well-known/gbo"),
	{"subject": {"id": actor, "type": "identity"}},
)

test_source_metadata_exact_route_allowed if {
	authz.allow with input as metadata_input("GET", "/.well-known/gbo")
}

test_source_metadata_simulation_peer_allowed if {
	authz.allow with input as metadata_input_for_actor("0000009961MINEZK0000")
}

test_source_metadata_other_actor_denied if {
	not authz.allow with input as metadata_input_for_actor("00000001234567890000")
}

test_source_metadata_wrong_method_denied if {
	not authz.allow with input as metadata_input("POST", "/.well-known/gbo")
}

test_source_metadata_wrong_path_denied if {
	not authz.allow with input as metadata_input("GET", "/.well-known/other")
}

# The metadata rule must not widen into the data path. An allowed metadata
# peer making a GraphQL request falls through to the rule-engine, which
# denies it on the evidence like any other request — the metadata branch
# does not short-circuit it to allow. It carries no consent token and names
# no subject, so the engine judges it a PID-regime request without a subject.
test_graphql_request_from_metadata_peer_falls_through_to_engine if {
	input_doc := {
		"subject": {"id": "99999999900000000100", "type": "identity"},
		"action": {"id": "POST", "type": "name"},
		"resource": {"id": "/graphql", "type": "uri"},
		"context": {
			"pip": {},
			"resolved": {
				"fields": [{
					"id": "aangifte.box1",
					"parent": "AangifteIH",
					"name": "box1Inkomen",
					"scalar": false,
				}],
				"args": {},
			},
		},
	}
	not authz.allow with input as input_doc
	reason := authz.reason with input as input_doc
	reason == "PID_NOT_PRESENT"
}

# `reason` is the only part of the decision detail OpenFTV transports: it
# becomes reason_user.en in the AuthZEN response, and so the reason the
# Authorization Decision Log records. It must be the policy's own reason
# code, not a summary of it.
test_deny_reason_is_the_reason_admin_code if {
	input_doc := {
		"subject": {"id": "99999999900000000100", "type": "identity"},
		"action": {"id": "POST", "type": "name"},
		"resource": {"id": "/graphql", "type": "uri"},
		"context": {"resolved": {"fields": [], "args": {}}},
	}
	resp := authz.response with input as input_doc
	not resp.decision
	reason := authz.reason with input as input_doc
	reason == resp.context.reason_admin.code
}

# The FSC Inway returns reason_user to the caller in its 401, so the reason
# is a code and never carries a value from the request.
test_deny_reason_is_a_bare_code if {
	reason := authz.reason with input as metadata_input("POST", "/.well-known/gbo")
	regex.match(`^[A-Z][A-Z0-9_]*$`, reason)
}

# An allow carries no reason; OpenFTV answers reason_user.en "ok".
test_allow_carries_no_reason if {
	not authz.reason with input as metadata_input("GET", "/.well-known/gbo")
}
