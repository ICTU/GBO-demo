package dvtp.gbo.rules.dvt0001_test

import data.dvtp.gbo.lib
import data.dvtp.gbo.rules.dvt0001

# ═══════════════════════════════════════════════════════════════════════════
# DVT0001 axes — consent + constraint-binding.
#
# Tests run against lib.evaluate(spec, ctx) to isolate the check-axes from
# the engine's field-binding. The spec is taken from the rule itself.
# ═══════════════════════════════════════════════════════════════════════════

# Minimal ctx-shape that carries a valid consent, a matching PI-binding,
# a year filter covered by the consent's scopes, and the query-argument
# that the constraint-binding checks. Overridden per test via object.union.
_base_ctx := {
	"subject": {"type": "org", "id": "99999999900000000300"},
	"args": {
		"bsn": "PI-abc123",
		"belastingjaren.0": "2025",
	},
	"time": "2026-07-06T12:00:00Z",
	"resource": {
		"scope": "bd:ib:2025",
		"pi": "PI-abc123",
	},
	"pip": {"consent": {
		"context_valid": true,
		"status_available": true,
		"exists": true,
		"withdrawn": false,
		"valid_until": "2030-01-01T00:00:00Z",
		"granted_scopes": ["bd:ib:2025"],
		"pi": "PI-abc123",
		"dienstverlener_oin": "99999999900000000300",
	}},
	"field": "Query.ingeschrevenPersoon.heeftBelastingjaarAangifte",
}

# ── Happy path ──────────────────────────────────────────────────────────

test_allow_valid_consent_and_binding if {
	result := lib.evaluate(dvt0001.spec, _base_ctx)
	result.decision == true
}

test_deny_invalid_signed_context if {
	ctx := object.union(_base_ctx, {"pip": {"consent": object.union(_base_ctx.pip.consent, {"context_valid": false})}})
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "CONSENT_CONTEXT_INVALID"
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
# Over a DelegatedServiceConnection grant the integrator's peer connects
# (subject.id) and the Inway names the service provider it connects for
# (subject.attributes.outway_delegator_peer_id). The consent binds the
# service provider; the integrator needs a mandate of its own, which the
# engine supplies from the admission register as pip.integrator.

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

# The integrator is registered for the provider it names, but the consent
# was given to another provider: the binding holds against the represented
# party, so a mandate for one provider cannot spend another's consent.
test_deny_delegated_call_under_another_providers_consent if {
	ctx := _delegated_ctx(_mandate)
	other := object.union(ctx, {"pip": {"consent": object.union(_base_ctx.pip.consent, {"dienstverlener_oin": "99999999900000000999"})}})
	result := lib.evaluate(dvt0001.spec, other)
	result.decision == false
	result.context.reason_admin.code == "CONSENT_ACTOR_MISMATCH"
}

# Nor can the integrator spend a consent given to itself on behalf of a
# provider it names: the recipient must be the represented party, not the
# peer that connects.
test_deny_delegated_call_under_a_consent_to_the_integrator if {
	ctx := _delegated_ctx(_mandate)
	own := object.union(ctx, {"pip": {"consent": object.union(_base_ctx.pip.consent, {"dienstverlener_oin": _integrator})}})
	result := lib.evaluate(dvt0001.spec, own)
	result.decision == false
	result.context.reason_admin.code == "CONSENT_ACTOR_MISMATCH"
}

# ── Consent-existence ───────────────────────────────────────────────────

test_deny_consent_not_found if {
	ctx := object.union(_base_ctx, {"pip": {"consent": {"exists": false}}})
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "CONSENT_NOT_FOUND"
}

# ── Consent-status ──────────────────────────────────────────────────────

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

# ── Scope-membership in consent ─────────────────────────────────────────

test_deny_scope_not_in_granted_scopes if {
	ctx := object.union(_base_ctx, {
		"resource": object.union(_base_ctx.resource, {"scope": "bd:ib:2024"}),
		"pip": {"consent": object.union(_base_ctx.pip.consent, {"granted_scopes": ["bd:ib:2025"]})},
	})
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "CONSENT_SCOPE_MISMATCH"
}

# ── Constraint-binding (PI in query-arg must equal resource.pi) ─────────

test_deny_constraint_mismatch if {
	ctx := object.union(_base_ctx, {"args": {"bsn": "PI-different", "belastingjaren.0": "2025"}})
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "CONSTRAINT_MISMATCH"
}

# ── Year-coverage (each requested year needs bd:ib:<year> in consent) ───

test_allow_multiple_years_all_consented if {
	ctx := object.union(_base_ctx, {
		"args": {"bsn": "PI-abc123", "belastingjaren.0": "2025", "belastingjaren.1": "2024"},
		"pip": {"consent": object.union(_base_ctx.pip.consent, {"granted_scopes": ["bd:ib:2025", "bd:ib:2024"]})},
	})
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == true
}

test_deny_year_not_consented if {
	# Consent covers 2025 only; the query asks for 2024 and 2025.
	ctx := object.union(_base_ctx, {"args": {"bsn": "PI-abc123", "belastingjaren.0": "2025", "belastingjaren.1": "2024"}})
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "YEAR_NOT_COVERED"
}

test_deny_year_filter_missing if {
	# No belastingjaren in the query — the bron would return all years,
	# so per-year policy cannot hold: fail closed. (object.union merges
	# recursively, so the key must be removed from the base ctx itself.)
	ctx := object.union(
		object.remove(_base_ctx, ["args"]),
		{"args": object.remove(_base_ctx.args, ["belastingjaren.0"])},
	)
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "YEAR_NOT_COVERED"
}

# ── Year-coverage via a GraphQL list variable ──────────────────────────
# `heeftBelastingjaarAangifte(belastingjaren: $jaren)` — the PDP stores
# the resolved variable whole under the un-suffixed key, not as
# belastingjaren.0/.1. Both shapes must be recognised.

test_allow_years_from_list_variable if {
	ctx := object.union(
		object.remove(_base_ctx, ["args"]),
		{
			"args": {"bsn": "PI-abc123", "belastingjaren": [2025]},
			"pip": {"consent": object.union(_base_ctx.pip.consent, {"granted_scopes": ["bd:ib:2025"]})},
		},
	)
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == true
}

test_deny_year_from_list_variable_not_consented if {
	ctx := object.union(
		object.remove(_base_ctx, ["args"]),
		{"args": {"bsn": "PI-abc123", "belastingjaren": [2024, 2025]}},
	)
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "YEAR_NOT_COVERED"
}

test_allow_year_from_scalar_variable if {
	ctx := object.union(
		object.remove(_base_ctx, ["args"]),
		{"args": {"bsn": "PI-abc123", "belastingjaren": 2025}},
	)
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == true
}
