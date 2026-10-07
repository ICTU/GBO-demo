package authz_test

import data.authz
import data.dvtp.gbo.fixtures_test as fx

# The metadata path is gated by subject, FSC service, method and path; each
# case below varies one of them.
metadata_input(method, path) := metadata_input_on("gbo-metadata-bd", method, path)

metadata_input_on(service, method, path) := {
	"subject": {"id": "99999999900000000100", "type": "identity", "attributes": {"service_name": service}},
	"action": {"id": method, "type": "name"},
	"resource": {"id": path, "type": "uri"},
	"context": {},
}

metadata_input_for_actor(actor) := object.union(
	metadata_input("GET", "/.well-known/gbo"),
	{"subject": {"id": actor}},
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

test_source_metadata_rvig_service_allowed if {
	authz.allow with input as metadata_input_on("gbo-metadata-rvig", "GET", "/.well-known/gbo")
}

# The same path on a data service is no metadata request: it is judged by
# the engine, which refuses a GET.
test_source_metadata_path_on_a_data_service_denied if {
	every service in ["bri", "brp", "lvg"] {
		input_doc := metadata_input_on(service, "GET", "/.well-known/gbo")
		not authz.allow with input as input_doc
	}
}

test_source_metadata_without_a_service_denied if {
	input_doc := object.union(metadata_input("GET", "/.well-known/gbo"), {"subject": {"attributes": {"service_name": null}}})
	not authz.allow with input as input_doc
	not authz.allow with input as object.remove(metadata_input("GET", "/.well-known/gbo"), ["subject"])
}

test_source_metadata_wrong_path_denied if {
	not authz.allow with input as metadata_input("GET", "/.well-known/other")
}

# A metadata peer making a GraphQL request is judged by the rule engine like
# any other request; the metadata rule must not short-circuit it to allow.
test_graphql_request_from_metadata_peer_falls_through_to_engine if {
	input_doc := fx.income("99999999900000000100", "", [2024], {})
	not authz.allow with input as input_doc
	reason := authz.reason with input as input_doc
	reason == "PID_NOT_PRESENT"
}

# `reason` is what the decision log records, so it must be the policy's own
# reason code, not a summary of it.
test_deny_reason_is_the_reason_admin_code if {
	input_doc := fx.request("99999999900000000100", "bri", [], {})
	resp := authz.response with input as input_doc
	not resp.decision
	reason := authz.reason with input as input_doc
	reason == resp.context.reason_admin.code
}

# The Inway returns the reason to the caller, so it must never carry request
# data.
test_deny_reason_is_a_bare_code if {
	reason := authz.reason with input as metadata_input("POST", "/.well-known/gbo")
	regex.match(`^[A-Z][A-Z0-9_]*$`, reason)
}

# An allow carries no reason; OpenFTV answers reason_user.en "ok".
test_allow_carries_no_reason if {
	not authz.reason with input as metadata_input("GET", "/.well-known/gbo")
}
