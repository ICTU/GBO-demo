package dvtp.gbo.rules.dvt0001_test

import data.dvtp.gbo.fixtures_test as fx
import data.dvtp.gbo.lib
import data.dvtp.gbo.rules.dvt0001

# ═══════════════════════════════════════════════════════════════════════════
# DVT0001 checks, through lib.evaluate with the rule's own spec, apart from
# the engine. Each ctx is one field: the consent checks hold on every field,
# the subject binding on the root field, the year check on
# heeftBelastingjaarAangifte.
# ═══════════════════════════════════════════════════════════════════════════

# A valid consent, on the field whose year filter it covers. _on moves the
# ctx to another field.
_base_ctx := {
	"subject": {"type": "org", "id": "99999999900000000300"},
	"time": "2026-07-06T12:00:00Z",
	"resource": {
		"scope": "bd:ib:2025",
		"subject_placeholders": {"consent:identity", "consent:pseudonym"},
	},
	"pip": {"consent": {
		"context_valid": true,
		"status_available": true,
		"exists": true,
		"withdrawn": false,
		"valid_until": "2030-01-01T00:00:00Z",
		"granted_scopes": ["bd:ib:2025"],
		"dienstverlener_oin": "99999999900000000300",
	}},
	"field": fx.declarations(fx.literal([2025])),
}

# object.union merges recursively, so a field is swapped, not merged.
_on(field) := object.union(object.remove(_base_ctx, ["field"]), {"field": field})

_on_person(bsn) := _on(fx.person(bsn))

_on_years(years_arg) := _on(fx.declarations(years_arg))

# ── Happy path ──────────────────────────────────────────────────────────

test_allow_valid_consent_and_binding if {
	result := lib.evaluate(dvt0001.spec, _base_ctx)
	result.decision == true
}

test_allow_on_the_root_field_with_the_identity_placeholder if {
	result := lib.evaluate(dvt0001.spec, _on_person("consent:identity"))
	result.decision == true
	_step_status(result, "CONSTRAINT_MISMATCH") == "pass"
	_step_status(result, "YEAR_NOT_COVERED") == "skipped"
}

# A leaf carries no argument; only the consent axes apply to it.
test_allow_on_a_leaf if {
	result := lib.evaluate(dvt0001.spec, _on(fx.amount("box1Inkomen")))
	result.decision == true
	_step_status(result, "CONSTRAINT_MISMATCH") == "skipped"
	_step_status(result, "YEAR_NOT_COVERED") == "skipped"
}

test_deny_invalid_signed_context if {
	ctx := object.union(_base_ctx, {"pip": {"consent": object.union(_base_ctx.pip.consent, {"context_valid": false})}})
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "CONSENT_CONTEXT_INVALID"
}

# The portal trace stops at the first failure, also when it is the very
# first check.
test_checks_after_the_first_failure_are_skipped if {
	ctx := object.union(_base_ctx, {"pip": {"consent": object.union(_base_ctx.pip.consent, {"context_valid": false})}})
	steps := lib.evaluate(dvt0001.spec, ctx).context.reason_admin.steps
	steps[0].status == "fail"
	every step in array.slice(steps, 1, count(steps)) {
		step.status == "skipped"
	}
}

test_deny_status_unavailable if {
	ctx := object.union(_base_ctx, {"pip": {"consent": object.union(_base_ctx.pip.consent, {"status_available": false})}})
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "CONSENT_STATUS_UNAVAILABLE"
}

test_deny_fsc_actor_does_not_match_signed_recipient if {
	ctx := object.union(_base_ctx, {"subject": {"type": "org", "id": "99999999900000000999"}})
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "CONSENT_ACTOR_MISMATCH"
}

# ── Delegated calls: an integrator acting for the service provider ──────
# The consent binds the provider (the delegator); the integrator (subject.id)
# needs its own mandate, supplied as pip.integrator.

_integrator := "99999999900000001100"

_mandate := {"active": true, "acts_for": [{"peer_id": "99999999900000000300", "rules": ["DVT0001"]}]}

_delegated_ctx(integrator_pip) := object.union(_base_ctx, {
	"subject": {
		"type": "identity",
		"id": _integrator,
		"attributes": {"outway_delegator_peer_id": "99999999900000000300"},
	},
	"pip": object.union(_base_ctx.pip, {"integrator": integrator_pip}),
})

_step_status(result, code) := status if {
	steps := object.get(result.context, "steps", object.get(object.get(result.context, "reason_admin", {}), "steps", []))
	some step in steps
	step.code == code
	status := step.status
}

test_allow_delegated_call_under_the_providers_consent if {
	result := lib.evaluate(dvt0001.spec, _delegated_ctx(_mandate))
	result.decision == true
	_step_status(result, "CONSENT_ACTOR_MISMATCH") == "pass"
	_step_status(result, "INTEGRATOR_NOT_REGISTERED") == "pass"
}

test_direct_call_skips_the_mandate if {
	result := lib.evaluate(dvt0001.spec, _base_ctx)
	result.decision == true
	_step_status(result, "INTEGRATOR_NOT_REGISTERED") == "skipped"
}

# The service provider cannot claim the delegator attribute for itself: a
# delegator equal to the connecting peer is a direct call.
test_delegator_equal_to_caller_is_a_direct_call if {
	ctx := object.union(_base_ctx, {"subject": {
		"type": "identity",
		"id": "99999999900000000300",
		"attributes": {"outway_delegator_peer_id": "99999999900000000300"},
	}})
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == true
	_step_status(result, "INTEGRATOR_NOT_REGISTERED") == "skipped"
}

test_deny_delegated_call_without_admission_entry if {
	ctx := object.union(_delegated_ctx({}), {"pip": {"consent": _base_ctx.pip.consent}})
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "INTEGRATOR_NOT_REGISTERED"
}

test_deny_delegated_call_for_an_unregistered_provider if {
	mandate := {"active": true, "acts_for": [{"peer_id": "99999999900000000999", "rules": ["DVT0001"]}]}
	result := lib.evaluate(dvt0001.spec, _delegated_ctx(mandate))
	result.decision == false
	result.context.reason_admin.code == "INTEGRATOR_NOT_REGISTERED"
}

test_deny_delegated_call_for_an_unregistered_rule if {
	mandate := {"active": true, "acts_for": [{"peer_id": "99999999900000000300", "rules": ["LVG0001"]}]}
	result := lib.evaluate(dvt0001.spec, _delegated_ctx(mandate))
	result.decision == false
	result.context.reason_admin.code == "INTEGRATOR_NOT_REGISTERED"
}

test_deny_delegated_call_from_a_suspended_integrator if {
	result := lib.evaluate(dvt0001.spec, _delegated_ctx(object.union(_mandate, {"active": false})))
	result.decision == false
	result.context.reason_admin.code == "INTEGRATOR_NOT_REGISTERED"
}

# A mandate for one provider cannot spend another provider's consent.
test_deny_delegated_call_under_another_providers_consent if {
	ctx := _delegated_ctx(_mandate)
	other := object.union(ctx, {"pip": {"consent": object.union(_base_ctx.pip.consent, {"dienstverlener_oin": "99999999900000000999"})}})
	result := lib.evaluate(dvt0001.spec, other)
	result.decision == false
	result.context.reason_admin.code == "CONSENT_ACTOR_MISMATCH"
}

# Nor can the integrator spend a consent given to itself: the recipient must
# be the represented party, not the peer that connects.
test_deny_delegated_call_under_a_consent_to_the_integrator if {
	ctx := _delegated_ctx(_mandate)
	own := object.union(ctx, {"pip": {"consent": object.union(_base_ctx.pip.consent, {"dienstverlener_oin": _integrator})}})
	result := lib.evaluate(dvt0001.spec, own)
	result.decision == false
	result.context.reason_admin.code == "CONSENT_ACTOR_MISMATCH"
}

# ── Consent exists ──────────────────────────────────────────────────────

test_deny_consent_not_found if {
	ctx := object.union(_base_ctx, {"pip": {"consent": {"exists": false}}})
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "CONSENT_NOT_FOUND"
}

# ── Consent status ──────────────────────────────────────────────────────

test_deny_consent_withdrawn if {
	ctx := object.union(_base_ctx, {"pip": {"consent": object.union(_base_ctx.pip.consent, {"withdrawn": true})}})
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "CONSENT_WITHDRAWN"
}

test_deny_consent_expired if {
	ctx := object.union(_base_ctx, {"pip": {"consent": object.union(_base_ctx.pip.consent, {"valid_until": "2020-01-01T00:00:00Z"})}})
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "CONSENT_EXPIRED"
}

# ── Scope in consent ────────────────────────────────────────────────────

test_deny_scope_not_in_granted_scopes if {
	ctx := object.union(_base_ctx, {
		"resource": object.union(_base_ctx.resource, {"scope": "bd:ib:2024"}),
		"pip": {"consent": object.union(_base_ctx.pip.consent, {"granted_scopes": ["bd:ib:2025"]})},
	})
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "CONSENT_SCOPE_MISMATCH"
}

# ── Constraint binding: the subject must be an accepted placeholder ─────
# A literal value is a subject the caller chose.

test_deny_literal_subject if {
	result := lib.evaluate(dvt0001.spec, _on_person("999991772"))
	result.decision == false
	result.context.reason_admin.code == "CONSTRAINT_MISMATCH"
}

# The income API takes a BSN; a pseudonym is stopped before the source.
test_deny_pseudonym_placeholder if {
	result := lib.evaluate(dvt0001.spec, _on_person("consent:pseudonym"))
	result.decision == false
	result.context.reason_admin.code == "CONSTRAINT_MISMATCH"
	result.context.reason_admin.expected == `Query.ingeschrevenPersoon.bsn in ["consent:identity"]`
}

# consent:subject is not a placeholder, so it counts as a literal.
test_deny_retired_placeholder if {
	result := lib.evaluate(dvt0001.spec, _on_person("consent:subject"))
	result.decision == false
	result.context.reason_admin.code == "CONSTRAINT_MISMATCH"
}

# A rule whose API takes both forms lists both placeholders.
_both_spec := object.union(dvt0001.spec, {"constraint_binding": [{
	"field": "Query.ingeschrevenPersoon",
	"arg": "bsn",
	"placeholders": {"consent:identity", "consent:pseudonym"},
}]})

test_allow_either_placeholder_when_the_rule_lists_both if {
	every placeholder in ["consent:identity", "consent:pseudonym"] {
		lib.evaluate(_both_spec, _on_person(placeholder)).decision == true
	}
}

# Listing a placeholder does not make it stand for someone: without a
# verified consent the engine provides none, and neither matches.
test_deny_listed_placeholder_that_stands_for_nobody if {
	ctx := object.union(_on_person("consent:identity"), {"resource": {"subject_placeholders": set()}})
	result := lib.evaluate(_both_spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "CONSTRAINT_MISMATCH"
}

# A placeholder from a schema default is no placeholder the caller wrote.
test_deny_subject_from_a_schema_default if {
	result := lib.evaluate(dvt0001.spec, _on(fx.person_as(["ingeschrevenPersoon"], fx.schema_default("consent:identity"))))
	result.decision == false
	result.context.reason_admin.code == "CONSTRAINT_MISMATCH"
}

# ── Years: each needs bd:ib:<year> in the consent ───────────────────────

test_allow_multiple_years_all_consented if {
	ctx := object.union(_on_years(fx.literal([2025, 2024])), {"pip": {"consent": object.union(_base_ctx.pip.consent, {"granted_scopes": ["bd:ib:2025", "bd:ib:2024"]})}})
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == true
}

test_deny_year_not_consented if {
	# Consent covers 2025 only; the query asks for 2024 and 2025.
	result := lib.evaluate(dvt0001.spec, _on_years(fx.literal([2025, 2024])))
	result.decision == false
	result.context.reason_admin.code == "YEAR_NOT_COVERED"
}

test_deny_year_filter_missing if {
	# Without a filter the source returns every year: fail closed.
	result := lib.evaluate(dvt0001.spec, _on(fx.declarations_without_years))
	result.decision == false
	result.context.reason_admin.code == "YEAR_NOT_COVERED"
}

# The source executes its own default, which need not be the bundled one.
test_deny_years_from_a_schema_default if {
	result := lib.evaluate(dvt0001.spec, _on_years(fx.schema_default([2025])))
	result.decision == false
	result.context.reason_admin.code == "YEAR_NOT_COVERED"
}

test_deny_years_with_an_explicit_null if {
	result := lib.evaluate(dvt0001.spec, _on_years(fx.variable("jaren", null)))
	result.decision == false
	result.context.reason_admin.code == "YEAR_NOT_COVERED"
}

# ── Years from a GraphQL variable ──────────────────────────────────────
# The mapper coerces a variable, or a list with one in it, to a list.

test_allow_years_from_list_variable if {
	result := lib.evaluate(dvt0001.spec, _on_years(fx.variable("jaren", [2025])))
	result.decision == true
}

test_deny_year_from_list_variable_not_consented if {
	result := lib.evaluate(dvt0001.spec, _on_years(fx.variable("jaren", [2024, 2025])))
	result.decision == false
	result.context.reason_admin.code == "YEAR_NOT_COVERED"
}

test_allow_years_from_a_variable_in_a_list if {
	mixed := {"value": [2025], "origin": "mixed", "variables": ["jaar"]}
	result := lib.evaluate(dvt0001.spec, _on_years(mixed))
	result.decision == true
}
