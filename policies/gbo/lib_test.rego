package gbo.lib_test

import data.gbo.fixtures_test as fx
import data.gbo.lib

# ── The declared scope ───────────────────────────────────────────────────

# The scope is the one the consumer declares, in any header-name case.
test_scope_is_the_declared_header if {
	scope := lib.declared_scope with input as {"subject": {"id": "x"}, "context": {"headers": {"x-gbo-scope": "bd:ib:2025"}}}
	scope == "bd:ib:2025"
}

# Two values under two spellings are not resolved by picking one.
test_scope_twice_is_no_scope if {
	scope := lib.declared_scope with input as {"subject": {"id": "x"}, "context": {"headers": {"X-Gbo-Scope": "bd:ib:2025", "x-gbo-scope": "bd:ib:2024"}}}
	scope == ""
}

# ── The fields ───────────────────────────────────────────────────────────

# The rules see every mapper record, __typename included (profile 9.4), at
# the position the engine gives it.
test_fields_are_every_mapper_record if {
	typename := {"path": ["ingeschrevenPersoon", "__typename"], "parentType": "IngeschrevenPersoon", "field": "__typename", "leaf": true}
	fields := array.concat(fx.income_fields("consent:identity", fx.literal([2025])), [typename])
	got := lib.fields with input as fx.request(fx.hv, "bri", fields, fx.scope_headers("bd:ib:2025"))
	got == fields
}
