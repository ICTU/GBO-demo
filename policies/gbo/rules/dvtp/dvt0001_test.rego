package gbo.rules.dvtp.dvt0001_test

import data.gbo.fixtures_test as fx
import data.gbo.rules.dvtp.dvt0001

# ═══════════════════════════════════════════════════════════════════════════
# DVT0001 on its own, apart from the engine: a request goes in, the rule's
# reasons to refuse come out. The consent checks hold for the request as a
# whole, the subject on the root field (position 0 of fx.income), the years
# on heeftBelastingjaarAangifte (position 1). The integrator's mandate is the
# engine's; engine_test.rego tests it.
# ═══════════════════════════════════════════════════════════════════════════

_consent := {
	"context_valid": true,
	"status_available": true,
	"exists": true,
	"withdrawn": false,
	"valid_until": "2030-01-01T00:00:00Z",
	"granted_scopes": ["bd:ib:2025"],
	"dienstverlener_oin": fx.hv,
}

_request := fx.income(fx.hv, "consent:identity", [2025], fx.scope_headers("bd:ib:2025"))

# The request with other fields, keeping the consumer and the scope.
_with_fields(fields) := object.union(_request, {"resource": {"attributes": {"graphql": {"fields": fields}}}})

_person(bsn_arg) := _with_fields([fx.person_as(["ingeschrevenPersoon"], bsn_arg), fx.declarations(fx.literal([2025]))])

_years(years_arg) := _with_fields([fx.person("consent:identity"), fx.declarations(years_arg)])

_with_consent(changes) := object.union(_consent, changes)

# The codes of the requirements that do not hold. A list that is undefined
# leaves this undefined too, so the test fails rather than pass empty.
_unmet(requirements) := {r.code | some r in requirements; not _holds(r)}

_holds(r) if r.holds == true

# Every reason the rule gives, for the request and for any field.
_reasons(req, consent) := r if {
	request := dvt0001.requirements with input as req with data.gbo.consent.resolved as consent
	fields := dvt0001.field_requirements with input as req with data.gbo.consent.resolved as consent
	count(fields) == count(object.get(req, ["resource", "attributes", "graphql", "fields"], []))
	r := _unmet(request) | {x | some checks in fields; some x in _unmet(checks)}
}

# The reasons for the field at position i.
_reasons_on(req, consent, i) := r if {
	request := dvt0001.requirements with input as req with data.gbo.consent.resolved as consent
	checks := dvt0001.field_requirements[i] with input as req with data.gbo.consent.resolved as consent
	r := _unmet(request) | _unmet(checks)
}

# ── Allowed ─────────────────────────────────────────────────────────────

test_allow_valid_consent_and_binding if {
	_reasons(_request, _consent) == set()
}

# A leaf carries no argument; only the consent applies to it.
test_allow_on_a_leaf if {
	_reasons_on(fx.income(fx.hv, "999991772", [2024], fx.scope_headers("bd:ib:2025")), _consent, 4) == set()
}

# ── The consent ─────────────────────────────────────────────────────────

test_deny_invalid_signed_context if {
	"CONSENT_CONTEXT_INVALID" in _reasons(_request, _with_consent({"context_valid": false}))
}

test_deny_status_unavailable if {
	"CONSENT_STATUS_UNAVAILABLE" in _reasons(_request, _with_consent({"status_available": false}))
}

test_deny_consent_not_found if {
	"CONSENT_NOT_FOUND" in _reasons(_request, _with_consent({"exists": false}))
}

test_deny_consent_withdrawn if {
	_reasons(_request, _with_consent({"withdrawn": true})) == {"CONSENT_WITHDRAWN"}
}

test_deny_consent_expired if {
	_reasons(_request, _with_consent({"valid_until": "2020-01-01T00:00:00Z"})) == {"CONSENT_EXPIRED"}
}

test_deny_scope_not_in_granted_scopes if {
	req := fx.income(fx.hv, "consent:identity", [2025], fx.scope_headers("bd:ib:2024"))
	"CONSENT_SCOPE_MISMATCH" in _reasons(req, _consent)
}

test_deny_fsc_actor_does_not_match_signed_recipient if {
	req := fx.income("99999999900000000999", "consent:identity", [2025], fx.scope_headers("bd:ib:2025"))
	_reasons(req, _consent) == {"CONSENT_ACTOR_MISMATCH"}
}

# ── Only the income question ────────────────────────────────────────────

# A consent for the ownership question, sent with its own scope, passes the
# consent checks; only the scope pin stops it on income fields.
test_deny_a_consent_for_another_question if {
	req := fx.request(fx.hv, "bri", [fx.person("consent:identity")], fx.scope_headers("lvg:vbo:eigendom"))
	_reasons(req, _with_consent({"granted_scopes": ["lvg:vbo:eigendom"]})) == {"SCOPE_NOT_ALLOWED"}
}

test_deny_a_scope_that_names_no_belastingjaar if {
	every scope in ["bd:ib:", "bd:ib:20x5"] {
		req := fx.request(fx.hv, "bri", [fx.person("consent:identity")], fx.scope_headers(scope))
		_reasons(req, _with_consent({"granted_scopes": [scope]})) == {"SCOPE_NOT_ALLOWED"}
	}
}

# ── Delegated calls: the consent binds the represented provider ─────────

_integrator := "99999999900000001100"

_delegated := object.union(_request, {"subject": {
	"id": _integrator,
	"attributes": {"outway_delegator_peer_id": fx.hv},
}})

test_allow_delegated_call_under_the_providers_consent if {
	_reasons(_delegated, _consent) == set()
}

# A mandate for one provider cannot spend another provider's consent.
test_deny_delegated_call_under_another_providers_consent if {
	_reasons(_delegated, _with_consent({"dienstverlener_oin": "99999999900000000999"})) == {"CONSENT_ACTOR_MISMATCH"}
}

# Nor can the integrator spend a consent given to itself: the recipient must
# be the represented party, not the peer that connects.
test_deny_delegated_call_under_a_consent_to_the_integrator if {
	_reasons(_delegated, _with_consent({"dienstverlener_oin": _integrator})) == {"CONSENT_ACTOR_MISMATCH"}
}

# ── The subject must be the identity placeholder ────────────────────────
# A literal value is a subject the caller chose.

test_deny_literal_subject if {
	_reasons_on(_person(fx.literal("999991772")), _consent, 0) == {"CONSTRAINT_MISMATCH"}
}

# The income API takes a BSN; a pseudonym is stopped before the source.
test_deny_pseudonym_placeholder if {
	_reasons_on(_person(fx.literal("consent:pseudonym")), _consent, 0) == {"CONSTRAINT_MISMATCH"}
}

# consent:subject is not a placeholder, so it counts as a literal.
test_deny_retired_placeholder if {
	_reasons_on(_person(fx.literal("consent:subject")), _consent, 0) == {"CONSTRAINT_MISMATCH"}
}

# A placeholder stands for someone only under a verified consent: without
# one the consent requirement refuses the field.
test_deny_placeholder_that_stands_for_nobody if {
	_reasons_on(_person(fx.literal("consent:identity")), _with_consent({"context_valid": false}), 0) == {"CONSENT_CONTEXT_INVALID"}
}

# A placeholder from a schema default is no placeholder the caller wrote.
test_deny_subject_from_a_schema_default if {
	_reasons_on(_person(fx.schema_default("consent:identity")), _consent, 0) == {"CONSTRAINT_MISMATCH"}
}

# The subject is judged on its own field only.
test_subject_reason_stays_on_the_root_field if {
	_reasons_on(_person(fx.literal("999991772")), _consent, 1) == set()
}

# ── Years: each needs bd:ib:<year> in the consent ───────────────────────

test_allow_multiple_years_all_consented if {
	consent := _with_consent({"granted_scopes": ["bd:ib:2025", "bd:ib:2024"]})
	_reasons(_years(fx.literal([2025, 2024])), consent) == set()
}

test_deny_year_not_consented if {
	# Consent covers 2025 only; the query asks for 2024 and 2025.
	_reasons_on(_years(fx.literal([2025, 2024])), _consent, 1) == {"YEAR_NOT_COVERED"}
}

test_deny_year_filter_missing if {
	# Without a filter the source returns every year: fail closed.
	req := _with_fields([fx.person("consent:identity"), fx.declarations_without_years])
	_reasons_on(req, _consent, 1) == {"YEAR_NOT_COVERED"}
}

# The source executes its own default, which need not be the bundled one.
test_deny_years_from_a_schema_default if {
	_reasons_on(_years(fx.schema_default([2025])), _consent, 1) == {"YEAR_NOT_COVERED"}
}

# A consent without granted_scopes covers no year.
test_deny_years_under_a_consent_without_scopes if {
	consent := object.remove(_consent, ["granted_scopes"])
	"YEAR_NOT_COVERED" in _reasons_on(_years(fx.literal([2025])), consent, 1)
}

test_deny_years_with_an_explicit_null if {
	_reasons_on(_years(fx.variable("jaren", null)), _consent, 1) == {"YEAR_NOT_COVERED"}
}

# ── Years from a GraphQL variable ──────────────────────────────────────
# The mapper coerces a variable, or a list with one in it, to a list.

test_allow_years_from_list_variable if {
	_reasons(_years(fx.variable("jaren", [2025])), _consent) == set()
}

test_deny_year_from_list_variable_not_consented if {
	_reasons_on(_years(fx.variable("jaren", [2024, 2025])), _consent, 1) == {"YEAR_NOT_COVERED"}
}

test_allow_years_from_a_variable_in_a_list if {
	mixed := {"value": [2025], "origin": "mixed", "variables": ["jaar"]}
	_reasons(_years(mixed), _consent) == set()
}
