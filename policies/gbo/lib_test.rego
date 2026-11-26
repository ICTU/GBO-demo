package gbo.lib_test

import data.gbo.fixtures_test as fx
import data.gbo.lib

# ── The fields ───────────────────────────────────────────────────────────

# The rules see every mapper record, __typename included (profile 9.4), at
# the position the engine gives it.
test_fields_are_every_mapper_record if {
	typename := {"path": ["ingeschrevenPersoon", "__typename"], "parentType": "IngeschrevenPersoon", "field": "__typename", "leaf": true}
	fields := array.concat(fx.income_fields("consent:identity", fx.literal([2025])), [typename])
	got := lib.fields with input as fx.request(fx.hv, "bri", fields, fx.headers)
	got == fields
}

# ── Where an argument's value comes from ────────────────────────────────

test_from_variable_only_for_a_supplied_variable if {
	lib.from_variable({"args": {"bsn": fx.variable("bsn", "consent:identity")}}, "bsn")
	not lib.from_variable({"args": {"bsn": fx.literal("consent:identity")}}, "bsn")
	not lib.from_variable({"args": {"bsn": {"value": "consent:identity", "origin": "default:bsn"}}}, "bsn")
	not lib.from_variable({"args": {"bsn": fx.schema_default("consent:identity")}}, "bsn")
	not lib.from_variable({"args": {}}, "bsn")
}
