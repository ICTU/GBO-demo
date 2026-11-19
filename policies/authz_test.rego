package authz_test

import data.authz
import data.gbo.fixtures_test as fx

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
	reason == "FIELD_NOT_PERMITTED"
}

# ── What the consumer is told: `reason` ──────────────────────────────────
# The consumer's code from the profile's decision context, plus the subcode
# of an unverifiable request. Never a field, never request data: the Inway
# returns it to the caller.

_reason_for(input_doc, c) := r if {
	r := authz.reason with input as input_doc with data.gbo.consent.resolved as c
}

_consent := {"context_valid": true, "status_available": true, "exists": true, "withdrawn": false, "valid_until": "2030-01-01T00:00:00Z", "granted_scopes": ["bd:ib:2025"], "dienstverlener_oin": fx.hv}

_unverifiable(code, subcode) := fx.request_with(
	fx.hv, "bri", {
		"profile": "ftv-graphql/0.1",
		"operation": null,
		"schema": {"digest": data.gbo.graphql_schemas.digests.bri},
		"fields": [],
		"unverifiable": {"code": code, "subcode": subcode, "message": "test"},
	},
	{},
)

test_reason_for_a_refused_field_names_no_field if {
	_reason_for(fx.income(fx.hv, "consent:identity", [2024], fx.headers), _consent) == "FIELD_NOT_PERMITTED"
}

# An unknown field reads as a refused one, so trying names maps nothing.
test_reason_for_an_unknown_field_hides_that_it_is_unknown if {
	_reason_for(_unverifiable("COVERAGE_UNVERIFIABLE", "INVALID_QUERY"), _consent) == "FIELD_NOT_PERMITTED"
}

test_reason_for_an_unverifiable_request_carries_the_subcode if {
	every subcode in ["PARSE_ERROR", "UNSUPPORTED_TRANSPORT", "INVALID_BODY", "LIMIT_EXCEEDED", "VARIABLE_ERROR", "OPERATION_AMBIGUOUS", "OPERATION_NOT_FOUND", "NO_OPERATION"] {
		_reason_for(_unverifiable("COVERAGE_UNVERIFIABLE", subcode), _consent) == concat(" ", ["COVERAGE_UNVERIFIABLE", subcode])
	}
}

test_reason_for_a_mutation if {
	input_doc := fx.request_with(fx.hv, "bri", {"profile": "ftv-graphql/0.1", "operation": {"type": "mutation", "name": null}, "schema": {"digest": data.gbo.graphql_schemas.digests.bri}, "fields": [], "unverifiable": {"code": "OPERATION_NOT_SUPPORTED", "message": "test"}}, {})
	_reason_for(input_doc, _consent) == "OPERATION_NOT_SUPPORTED"
}

test_reason_for_typename_alone if {
	typename := {"path": ["__typename"], "parentType": "Query", "field": "__typename", "leaf": true}
	_reason_for(fx.request(fx.hv, "bri", [typename], {}), _consent) == "NO_DATA_FIELDS"
}

# Our configuration errors are not the consumer's to know about.
test_reason_for_a_configuration_error_is_access_denied if {
	unpinned := fx.request_with(fx.hv, "onbekend", object.union(fx.graphql("bri", fx.income_fields("consent:identity", fx.literal([2025]))), {}), fx.headers)
	_reason_for(unpinned, _consent) == "ACCESS_DENIED"
	_reason_for(_unverifiable("CONFIG_ERROR", "SCHEMA_UNAVAILABLE"), _consent) == "ACCESS_DENIED"
	_reason_for(object.remove(fx.request(fx.hv, "bri", [], {}), ["resource"]), _consent) == "ACCESS_DENIED"
}

# An unreachable register outranks a field refused for the consumer's own
# reason: the request did not fail on what the consumer asked.
test_reason_for_an_unreachable_register_is_access_denied if {
	fields := array.concat(fx.income_fields("consent:identity", fx.literal([2025])), [fx.box("box2Inkomen")])
	input_doc := fx.request(fx.hv, "bri", fields, fx.headers)
	_reason_for(input_doc, object.union(_consent, {"status_available": false})) == "ACCESS_DENIED"
}

# The Inway returns the reason to the caller: a code, and a subcode at most.
test_deny_reason_is_a_bare_code if {
	reason := authz.reason with input as metadata_input("POST", "/.well-known/gbo")
	regex.match(`^[A-Z][A-Z0-9_]*( [A-Z][A-Z0-9_]*)?$`, reason)
}

# An allow carries no reason; OpenFTV answers reason_user.en "ok".
test_allow_carries_no_reason if {
	not authz.reason with input as metadata_input("GET", "/.well-known/gbo")
}

# ── LDV_READ_FSC: the register reads a component logbook (#686) ──────────

_trace_id := "4bf92f3577b34da6a3ce929d0e0e4736"

ldv_read_input(peer, service, method, path, body) := {
	"subject": {"id": peer, "type": "identity", "attributes": {"service_name": service}},
	"action": {"id": method, "type": "name", "attributes": {"body": body}},
	"resource": {"id": path, "type": "uri"},
	"context": {},
}

ldv_read(body) := ldv_read_input("99999999900000000700", "ldv-lezen-bd", "POST", "/data-processing-operations", body)

test_ldv_read_by_one_trace_id_allowed if {
	every service in ["ldv-lezen-bd", "ldv-lezen-brp", "ldv-lezen-lvg"] {
		authz.allow with input as ldv_read_input("99999999900000000700", service, "POST", "/data-processing-operations", json.marshal({"traceId": _trace_id}))
	}
}

test_ldv_read_without_a_trace_id_denied if {
	every body in [
		json.marshal({}),
		json.marshal({"dpl": {"core": {"dataSubjectId": "LP-x"}}}),
		json.marshal({"startTime": "2026-11-19T00:00:00Z"}),
		json.marshal({"dpl": {"core": {"processingActivityId": "https://x/v1"}}}),
	] {
		not authz.allow with input as ldv_read(body)
	}
}

test_ldv_read_with_a_trace_id_and_another_selector_denied if {
	not authz.allow with input as ldv_read(json.marshal({"traceId": _trace_id, "startTime": "2026-11-19T00:00:00Z"}))
}

test_ldv_read_with_a_uuid_or_malformed_trace_id_denied if {
	every trace in ["4bf92f35-77b3-4da6-a3ce-929d0e0e4736", "4BF92F3577B34DA6A3CE929D0E0E4736", "abc", ""] {
		not authz.allow with input as ldv_read(json.marshal({"traceId": trace}))
	}
	not authz.allow with input as ldv_read("not json")
	not authz.allow with input as ldv_read(json.marshal([_trace_id]))
}

test_ldv_read_by_another_peer_denied if {
	not authz.allow with input as ldv_read_input("99999999900000000300", "ldv-lezen-bd", "POST", "/data-processing-operations", json.marshal({"traceId": _trace_id}))
}

test_ldv_read_on_another_path_or_method_denied if {
	not authz.allow with input as ldv_read_input("99999999900000000700", "ldv-lezen-bd", "POST", "/logboek/records", json.marshal({"traceId": _trace_id}))
	not authz.allow with input as ldv_read_input("99999999900000000700", "ldv-lezen-bd", "GET", "/data-processing-operations", json.marshal({"traceId": _trace_id}))
}

# The register's peer has no business on a data service through this rule.
test_ldv_read_body_on_a_data_service_is_judged_as_graphql if {
	input_doc := ldv_read_input("99999999900000000700", "bri", "POST", "/data-processing-operations", json.marshal({"traceId": _trace_id}))
	not authz.allow with input as input_doc
	authz.reason with input as input_doc != "LDV_READ_NOT_PERMITTED"
}

test_ldv_read_refusal_has_its_own_code if {
	authz.reason with input as ldv_read(json.marshal({})) == "LDV_READ_NOT_PERMITTED"
}
