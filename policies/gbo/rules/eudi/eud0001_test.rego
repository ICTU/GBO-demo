package gbo.rules.eudi.eud0001_test

import data.gbo.fixtures_test as fx
import data.gbo.rules.eudi.eud0001

# ═══════════════════════════════════════════════════════════════════════════
# EUD0001 on its own, apart from the engine: a request without a consent
# token goes in, the rule's reasons come out. The subject is on the root
# field (position 0 of fx.income), the years on heeftBelastingjaarAangifte
# (position 1).
# ═══════════════════════════════════════════════════════════════════════════

_issuer := "00000004000000004000"

_request(actor, bsn, years_arg) := fx.request(actor, "bri", [fx.person(bsn), fx.declarations(years_arg), fx.year], {})

_pid_request := _request(_issuer, "999991772", fx.literal([2025]))

_reasons(req) := r if {
	r := eud0001.deny | {x | some reasons in eud0001.deny_field; some x in reasons} with input as req
}

_reasons_on(req, i) := r if {
	r := eud0001.deny | object.get(eud0001.deny_field, i, set()) with input as req
}

# ── Allowed ─────────────────────────────────────────────────────────────

test_allow_valid_actor_year_pid if {
	_reasons(_pid_request) == set()
}

test_allow_simulation_eudi_issuer if {
	_reasons(_request("0000009961MINEZK0000", "999991772", fx.literal([2025]))) == set()
}

# ── Years ──────────────────────────────────────────────────────────────

test_deny_year_not_allowed if {
	_reasons_on(_request(_issuer, "999991772", fx.literal([2023])), 1) == {"YEAR_NOT_ALLOWED"}
}

# The EUDI query writes the year as a variable in a list.
test_allow_year_from_a_variable_in_a_list if {
	mixed := {"value": [2024], "origin": "mixed", "variables": ["jaar"]}
	_reasons(_request(_issuer, "999991772", mixed)) == set()
}

test_deny_year_missing if {
	req := fx.request(_issuer, "bri", [fx.person("999991772"), fx.declarations_without_years], {})
	_reasons_on(req, 1) == {"YEAR_NOT_ALLOWED"}
}

test_deny_year_from_a_schema_default if {
	_reasons_on(_request(_issuer, "999991772", fx.schema_default([2025])), 1) == {"YEAR_NOT_ALLOWED"}
}

# The year is judged on its own field only.
test_year_reason_stays_on_its_field if {
	_reasons_on(_request(_issuer, "999991772", fx.literal([2023])), 0) == set()
}

# ── Actor ───────────────────────────────────────────────────────────────

test_deny_actor_not_in_allowed_actors if {
	_reasons(_request("00000001234567890000", "999991772", fx.literal([2025]))) == {"ACTOR_NOT_ALLOWED"}
}

# ── PID regime: no consent token, and a subject named ───────────────────

test_deny_pid_missing if {
	_reasons_on(_request(_issuer, "", fx.literal([2025])), 0) == {"PID_NOT_PRESENT"}
}

test_deny_placeholder_without_consent if {
	every placeholder in ["consent:identity", "consent:pseudonym"] {
		_reasons_on(_request(_issuer, placeholder, fx.literal([2025])), 0) == {"PID_NOT_PRESENT"}
	}
}

# Any consent token, verified or not, puts the request outside the PID
# regime, on every field.
test_deny_when_a_consent_token_was_presented if {
	r := eud0001.deny with input as _pid_request with data.gbo.consent.resolved as {"context_valid": false}
	r == {"PID_NOT_PRESENT"}
}
