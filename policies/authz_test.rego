package authz_test

import data.authz

metadata_input(flow, method, path) := {
	"subject": {"id": "99999999900000000100", "type": "identity"},
	"action": {"id": method, "type": "name"},
	"resource": {"id": path, "type": "uri"},
	"context": {"flow": flow},
}

test_source_metadata_exact_route_allowed if {
	authz.allow with input as metadata_input(
		"gbo:source-metadata",
		"GET",
		"/.well-known/gbo-attestations",
	)
}

test_source_metadata_wrong_flow_denied if {
	not authz.allow with input as metadata_input(
		"eudi:attestation",
		"GET",
		"/.well-known/gbo-attestations",
	)
}

test_source_metadata_wrong_method_denied if {
	not authz.allow with input as metadata_input(
		"gbo:source-metadata",
		"POST",
		"/.well-known/gbo-attestations",
	)
}

test_source_metadata_wrong_path_denied if {
	not authz.allow with input as metadata_input(
		"gbo:source-metadata",
		"GET",
		"/.well-known/other",
	)
}
