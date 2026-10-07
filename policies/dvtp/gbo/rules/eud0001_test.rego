package dvtp.gbo.rules.eud0001_test

import data.dvtp.gbo.fixtures_test as fx
import data.dvtp.gbo.lib
import data.dvtp.gbo.rules.eud0001

# ═══════════════════════════════════════════════════════════════════════════
# EUD0001 axes — concrete query year + actor + PID.
#
# Tests run against lib.evaluate(spec, ctx) to isolate the check-axes from
# the engine's field-binding. The spec is taken from the rule itself so
# that allowed_years / allowed_actors come from a single source.
# ═══════════════════════════════════════════════════════════════════════════

# Minimal ctx-shape that all EUD0001-checks can handle, on the field that
# carries the years. Overridden per test via object.union; _on puts the ctx
# on another field.
_base_ctx := {
	"subject": {"type": "org", "id": "00000004000000004000"},
	"time": "2026-07-06T12:00:00Z",
	"resource": {"scope": ""},
	"pip": {},
	"field": fx.declarations(fx.literal([2025])),
}

_on(field) := object.union(object.remove(_base_ctx, ["field"]), {"field": field})

# ── Happy path ──────────────────────────────────────────────────────────

test_allow_valid_actor_year_pid if {
	result := lib.evaluate(eud0001.spec, _base_ctx)
	result.decision == true
}

test_allow_on_the_root_field_naming_the_subject if {
	result := lib.evaluate(eud0001.spec, _on(fx.person("999991772")))
	result.decision == true
}

test_allow_simulation_eudi_issuer if {
	ctx := object.union(_base_ctx, {"subject": {"type": "org", "id": "0000009961MINEZK0000"}})
	result := lib.evaluate(eud0001.spec, ctx)
	result.decision == true
}

# ── Direct year authorization ──────────────────────────────────────────

test_deny_year_not_allowed if {
	result := lib.evaluate(eud0001.spec, _on(fx.declarations(fx.literal([2023]))))
	result.decision == false
	result.context.reason_admin.code == "YEAR_NOT_ALLOWED"
}

# The EUDI query writes the year as a variable in a list.
test_allow_year_from_a_variable_in_a_list if {
	mixed := {"value": [2024], "origin": "mixed", "variables": ["jaar"]}
	result := lib.evaluate(eud0001.spec, _on(fx.declarations(mixed)))
	result.decision == true
}

test_deny_year_missing if {
	result := lib.evaluate(eud0001.spec, _on(fx.declarations_without_years))
	result.decision == false
	result.context.reason_admin.code == "YEAR_NOT_ALLOWED"
}

test_deny_year_from_a_schema_default if {
	result := lib.evaluate(eud0001.spec, _on(fx.declarations(fx.schema_default([2025]))))
	result.decision == false
	result.context.reason_admin.code == "YEAR_NOT_ALLOWED"
}

# ── Actor-authorization ─────────────────────────────────────────────────

test_deny_actor_not_in_allowed_actors if {
	ctx := object.union(_base_ctx, {"subject": {"type": "org", "id": "00000001234567890000"}})
	result := lib.evaluate(eud0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "ACTOR_NOT_ALLOWED"
}

# ── PID-regime basis: no consent token, and a subject named ─────────────

test_deny_pid_missing if {
	result := lib.evaluate(eud0001.spec, _on(fx.person("")))
	result.decision == false
	result.context.reason_admin.code == "PID_NOT_PRESENT"
}

test_deny_placeholder_without_consent if {
	every placeholder in ["consent:identity", "consent:pseudonym"] {
		result := lib.evaluate(eud0001.spec, _on(fx.person(placeholder)))
		result.decision == false
		result.context.reason_admin.code == "PID_NOT_PRESENT"
	}
}

test_deny_when_a_consent_token_was_presented if {
	# A consent token puts the request under the consent regime, whether or
	# not it verified; the PID-regime basis must then fail.
	ctx := object.union(_base_ctx, {"pip": {"consent": {"context_valid": false}}})
	result := lib.evaluate(eud0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "PID_NOT_PRESENT"
}

# ── Axis activation is conditional on rule-declaration ─────────────────
# Each policy-path must carry scope- and actor-authorization, but the
# source per path differs. DVT0001 carries its scope-authorization via
# consent-scope-cover (rule-owned source) and declares no allowed_scopes
# — the scope-axis must then be silent, otherwise DVT0001 would break on
# every request. Same pattern for actor.

_rule_without_whitelists := {
	"rule_id": "TEST_NO_WHITELISTS",
	"consent_required": false,
	"consent_must_cover_scope": false,
	"pid_required": true,
	"subject_argument": {"field": "Query.ingeschrevenPersoon", "arg": "bsn"},
}

# A PID rule that names no argument for the subject has no field that names
# one: it fails closed rather than skip the subject.
test_pid_rule_without_subject_argument_fails_closed if {
	spec := object.remove(_rule_without_whitelists, ["subject_argument"])
	every field in [fx.person("999991772"), fx.declarations(fx.literal([2025])), fx.year] {
		result := lib.evaluate(spec, _on(field))
		result.context.reason_admin.code == "PID_NOT_PRESENT"
	}
}

# A year check that names no argument finds no years: it fails closed.
test_year_rule_without_years_argument_fails_closed if {
	spec := object.remove(eud0001.spec, ["years_argument"])
	result := lib.evaluate(spec, _base_ctx)
	result.decision == false
	result.context.reason_admin.code == "YEAR_NOT_ALLOWED"
}

test_scope_axis_inactive_without_whitelist if {
	ctx := object.union(_base_ctx, {"resource": {"scope": "anything-goes"}})
	result := lib.evaluate(_rule_without_whitelists, ctx)
	result.decision == true
}

test_actor_axis_inactive_without_whitelist if {
	ctx := object.union(_base_ctx, {"subject": {"type": "org", "id": "some-other-oin"}})
	result := lib.evaluate(_rule_without_whitelists, ctx)
	result.decision == true
}
