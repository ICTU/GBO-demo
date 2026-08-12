package dvtp.gbo.rules.eud0001_test

import data.dvtp.gbo.lib
import data.dvtp.gbo.rules.eud0001

# ═══════════════════════════════════════════════════════════════════════════
# EUD0001 axes — concrete query year + actor + PID.
#
# Tests run against lib.evaluate(spec, ctx) to isolate the check-axes from
# the engine's field-binding. The spec is taken from the rule itself so
# that allowed_years / allowed_actors come from a single source.
# ═══════════════════════════════════════════════════════════════════════════

# Minimal ctx-shape that all EUD0001-checks can handle. Overridden per
# test via object.union.
_base_ctx := {
	"subject": {"type": "org", "id": "00000004000000004000"},
	"args": {"belastingjaren.0": "2025"},
	"time": "2026-07-06T12:00:00Z",
	"resource": {"scope": ""},
	"pip": {"pid": {"pi": "PI-2f1a7c9b40e6d853"}},
	"field": "Query.ingeschrevenPersoon.heeftBelastingjaarAangifte",
}

# ── Happy path ──────────────────────────────────────────────────────────

test_allow_valid_actor_year_pid if {
	result := lib.evaluate(eud0001.spec, _base_ctx)
	result.decision == true
}

test_allow_simulation_eudi_issuer if {
	ctx := object.union(_base_ctx, {"subject": {"type": "org", "id": "0000009961MINEZK0000"}})
	result := lib.evaluate(eud0001.spec, ctx)
	result.decision == true
}

# ── Direct year authorization ──────────────────────────────────────────

test_deny_year_not_allowed if {
	ctx := object.union(_base_ctx, {"args": {"belastingjaren.0": "2023"}})
	result := lib.evaluate(eud0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "YEAR_NOT_ALLOWED"
}

test_deny_non_numeric_year if {
	ctx := object.union(_base_ctx, {"args": {"belastingjaren.0": "2023x"}})
	result := lib.evaluate(eud0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "YEAR_NOT_ALLOWED"
}

test_deny_year_missing if {
	ctx := object.union(object.remove(_base_ctx, ["args"]), {"args": {}})
	result := lib.evaluate(eud0001.spec, ctx)
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

# ── PID-check remains present ───────────────────────────────────────────

test_deny_pid_missing if {
	# object.union is deep-merged; explicit empty bsn instead of pip=={}.
	ctx := object.union(_base_ctx, {"pip": {"pid": {"pi": ""}}})
	result := lib.evaluate(eud0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "PID_NOT_PRESENT"
}

test_deny_pid_invalid_shape if {
	ctx := object.union(_base_ctx, {"pip": {"pid": {"pi": "abc"}}})
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
	"pip": null,
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
