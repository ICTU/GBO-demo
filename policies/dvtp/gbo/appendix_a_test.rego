package dvtp.gbo.appendix_a_test

import data.dvtp.gbo
import data.dvtp.gbo.fixtures_test as fx

# ═══════════════════════════════════════════════════════════════════════════
# The decision vectors of the FTV GraphQL profile's Appendix A (draft-01),
# run with the appendix's rule r-0001 on its fictional schema. Each vector's
# mapper output is the one services/ftv-graphql-mapper/vectors_test.go checks.
#
# Vectors that turn on a consented bsn or a peildatum are left out: GBO has
# no such check. Field codes are GBO's, not the appendix's reference codes.
# ═══════════════════════════════════════════════════════════════════════════

_digest := "sha256:appendix-a"

_r0001 := {
	"rule_id": "r-0001",
	"covers_types": {"Persoon", "Inkomen", "LoonInkomen", "Bedrag", "Adres"},
	"covers_fields": {"Query.persoon", "Persoon.inkomens", "Inkomen.bedrag", "LoonInkomen.bedrag", "Persoon.adres"},
	"spec": {
		"rule_id": "r-0001",
		"allowed_years": {y | some y in numbers.range(2000, 2024)},
		"years_argument": {"field": "Persoon.inkomens", "arg": "jaren"},
	},
}

_output(fields) := {
	"profile": "ftv-graphql/0.1",
	"operation": {"type": "query", "name": null},
	"schema": {"digest": _digest},
	"fields": fields,
	"unverifiable": null,
}

_decide_output(output, rules) := result if {
	result := gbo.response with input as fx.request_with("00000001000000000001", "appendix-a", output, {})
		with data.dvtp.gbo.rules as rules
		with data.dvtp.gbo.graphql_schemas.digests as {"appendix-a": _digest}
}

_decide(fields) := _decide_output(_output(fields), {"r0001": _r0001})

_denied(result) := [[d.index, d.key, d.code] | some d in result.context.denied_fields]

# ── Records ──────────────────────────────────────────────────────────────

_rec(path, parent, field, leaf) := {"path": path, "parentType": parent, "field": field, "leaf": leaf}

_on(record, type) := object.union(record, {"on": type})

_args(record, args) := object.union(record, {"args": args})

_persoon := _args(_rec(["persoon"], "Query", "persoon", false), {"bsn": fx.literal("999990011")})

_inkomens(jaren) := _args(_rec(["persoon", "inkomens"], "Persoon", "inkomens", false), {"jaren": jaren})

_jaar := _rec(["persoon", "inkomens", "jaar"], "Inkomen", "jaar", true)

_bedrag(parent) := _rec(["persoon", "inkomens", "bedrag"], parent, "bedrag", false)

_waarde := _rec(["persoon", "inkomens", "bedrag", "waarde"], "Bedrag", "waarde", true)

_valuta := _rec(["persoon", "inkomens", "bedrag", "valuta"], "Bedrag", "valuta", true)

_mixed(value, variable) := {"value": value, "origin": "mixed", "variables": [variable]}

# A.1, Section 6.2.
_a1 := [
	_args(_rec(["persoon"], "Query", "persoon", false), {"bsn": fx.variable("bsn", "999990011")}),
	_inkomens(_mixed([2024], "jaar")),
	_jaar,
	_on(_bedrag("LoonInkomen"), "LoonInkomen"),
	_waarde,
	_valuta,
]

# ── The vectors ──────────────────────────────────────────────────────────

test_a1_worked_example_allows if {
	_decide(_a1).decision == true
}

test_a2_one_field_too_many_denies_the_edge_and_its_leaf if {
	fields := array.concat(_a1, [
		_on(_rec(["persoon", "inkomens", "werkgever"], "LoonInkomen", "werkgever", false), "LoonInkomen"),
		_rec(["persoon", "inkomens", "werkgever", "naam"], "Werkgever", "naam", true),
	])
	result := _decide(fields)
	result.decision == false
	_denied(result) == [[6, "LoonInkomen.werkgever", "NO_APPLICABLE_RULE"], [7, "Werkgever.naam", "NO_APPLICABLE_RULE"]]
}

test_a4_an_alias_does_not_change_the_key if {
	fields := array.concat(_a1, [
		object.union(_on(_rec(["persoon", "inkomens", "w"], "LoonInkomen", "werkgever", false), "LoonInkomen"), {"alias": "w"}),
		_rec(["persoon", "inkomens", "w", "naam"], "Werkgever", "naam", true),
	])
	result := _decide(fields)
	_denied(result) == [[6, "LoonInkomen.werkgever", "NO_APPLICABLE_RULE"], [7, "Werkgever.naam", "NO_APPLICABLE_RULE"]]
	result.context.denied_fields[0].field == "persoon.inkomens.w"
}

test_a7_variable_default_is_judged if {
	allowed := [_persoon, _inkomens(_mixed([2019], "jaar")), _jaar]
	_decide(allowed).decision == true
	denied := [_persoon, _inkomens(_mixed([2025], "jaar")), _jaar]
	_denied(_decide(denied)) == [[1, "Persoon.inkomens", "YEAR_NOT_ALLOWED"]]
}

test_a8_variable_used_directly_resolved_to_its_default if {
	jaren := {"value": [2024], "origin": "default:jaren", "variables": ["jaren"]}
	_decide([_persoon, _inkomens(jaren), _jaar]).decision == true
}

test_a9_schema_argument_default_denies if {
	result := _decide([_persoon, _inkomens(fx.schema_default([2024])), _jaar])
	result.decision == false
	_denied(result) == [[1, "Persoon.inkomens", "YEAR_NOT_ALLOWED"]]
}

test_a26_introspection_only_denies_the_root_field if {
	result := _decide([
		_rec(["__schema"], "Query", "__schema", false),
		_rec(["__schema", "types"], "__Schema", "types", false),
		_rec(["__schema", "types", "name"], "__Type", "name", true),
	])
	_denied(result) == [[0, "Query.__schema", "NO_APPLICABLE_RULE"]]
}

test_a26_typename_alone_has_no_data_fields if {
	result := _decide([_rec(["__typename"], "Query", "__typename", true)])
	result.context.reason_admin == {"code": "NO_DATA_FIELDS"}
}

test_a31_the_concrete_type_the_rule_does_not_name_denies if {
	result := _decide([
		_persoon,
		_inkomens(fx.literal([2024])),
		_on(_bedrag("LoonInkomen"), "LoonInkomen"),
		_waarde,
		_on(_bedrag("WinstInkomen"), "WinstInkomen"),
		_valuta,
	])
	_denied(result) == [[4, "WinstInkomen.bedrag", "NO_APPLICABLE_RULE"]]
}

test_a32_union_with_typename_only_denies_the_root if {
	result := _decide([
		_args(_rec(["zoek"], "Query", "zoek", false), {"term": fx.literal("*")}),
		_rec(["zoek", "__typename"], "Zoekresultaat", "__typename", true),
	])
	_denied(result) == [[0, "Query.zoek", "NO_APPLICABLE_RULE"]]
}

# A root field binds by its key only, even when a rule covers Query.
test_a33_root_leaf_never_inherits if {
	through_query := object.union(_r0001, {"covers_types": _r0001.covers_types | {"Query"}})
	result := _decide_output(_output([_rec(["aantalPersonen"], "Query", "aantalPersonen", true)]), {"r0001": through_query})
	_denied(result) == [[0, "Query.aantalPersonen", "NO_APPLICABLE_RULE"]]
}

test_a36_introspection_next_to_a_permitted_field_denies if {
	result := _decide([
		_persoon,
		_rec(["persoon", "naam"], "Persoon", "naam", true),
		_rec(["__schema"], "Query", "__schema", false),
		_rec(["__schema", "types"], "__Schema", "types", false),
		_rec(["__schema", "types", "name"], "__Type", "name", true),
	])
	_denied(result) == [[2, "Query.__schema", "NO_APPLICABLE_RULE"]]
}

test_a37_leaf_with_arguments_does_not_inherit if {
	result := _decide([
		_persoon,
		_args(_rec(["persoon", "naam"], "Persoon", "naam", true), {"formaat": fx.literal("kort")}),
	])
	_denied(result) == [[1, "Persoon.naam", "NO_APPLICABLE_RULE"]]
}

test_a39_interface_key_without_type_condition_allows if {
	result := _decide([
		_persoon,
		_inkomens(fx.literal([2024])),
		_jaar,
		_bedrag("Inkomen"),
		_waarde,
	])
	result.decision == true
}

# ── Unverifiable requests and a missing or foreign schema ────────────────

_failed(code, subcode) := object.union(_output([]), {"unverifiable": {"code": code, "subcode": subcode, "message": "test"}})

test_a3_a10_unverifiable_requests_deny_with_the_mappers_code if {
	every subcode in ["INVALID_QUERY", "OPERATION_AMBIGUOUS", "PARSE_ERROR", "UNSUPPORTED_TRANSPORT"] {
		result := _decide_output(_failed("COVERAGE_UNVERIFIABLE", subcode), {"r0001": _r0001})
		result.context.reason_admin == {"code": "COVERAGE_UNVERIFIABLE", "subcode": subcode}
	}
}

test_a13_mutation_denies if {
	output := object.union(_output([]), {"unverifiable": {"code": "OPERATION_NOT_SUPPORTED", "message": "operation type mutation"}})
	result := _decide_output(output, {"r0001": _r0001})
	result.context.reason_admin == {"code": "OPERATION_NOT_SUPPORTED"}
}

test_a24_schema_missing_denies if {
	output := object.union(_failed("CONFIG_ERROR", "SCHEMA_UNAVAILABLE"), {"schema": null})
	result := _decide_output(output, {"r0001": _r0001})
	result.context.reason_admin == {"code": "CONFIG_ERROR", "subcode": "SCHEMA_UNAVAILABLE"}
}

test_a25_pinned_digest_mismatch_denies if {
	output := object.union(_output(_a1), {"schema": {"digest": "sha256:9f2c"}})
	result := _decide_output(output, {"r0001": _r0001})
	result.context.reason_admin == {"code": "CONFIG_ERROR", "subcode": "SCHEMA_MISMATCH"}
}

test_a40_mapper_output_missing_denies if {
	result := gbo.response with input as {"subject": {"id": "x", "attributes": {"service_name": "appendix-a"}}, "resource": {"type": "uri", "id": "/graphql"}, "context": {}}
		with data.dvtp.gbo.rules as {"r0001": _r0001}
	result.context.reason_admin == {"code": "CONFIG_ERROR", "subcode": "MAPPER_OUTPUT_MISSING"}
}
