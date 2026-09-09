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
# does not short-circuit it to allow.
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
	reason == "CONSENT_CONTEXT_INVALID"
}
