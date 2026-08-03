package dvtp.gbo.rules.eud0002_test

import data.dvtp.gbo.lib
import data.dvtp.gbo.rules.eud0001
import data.dvtp.gbo.rules.eud0002

# ═══════════════════════════════════════════════════════════════════════════
# EUD0002 axes — scope + actor + PID, without the BD path's year-axis.
#
# Same approach as eud0001_test: evaluate lib.evaluate(spec, ctx) directly so
# the axes are isolated from the engine's field-binding, with the spec taken
# from the rule itself.
# ═══════════════════════════════════════════════════════════════════════════

_base_ctx := {
	"subject": {"type": "org", "id": "00000004000000004000"},
	"args": {"vars.bsn": "999991772"},
	"time": "2026-07-27T12:00:00Z",
	"resource": {"scope": "brp:akte:overlijden"},
	"pip": {"pid": {"pi": "PI-9c4e10b7a6d2f385"}},
	"field": "Query.ingeschrevenPersoon.heeftHuwelijk.partners.datumOverlijden",
}

# ── Happy path ──────────────────────────────────────────────────────────

test_allow_valid_actor_scope_pid if {
	result := lib.evaluate(eud0002.spec, _base_ctx)
	result.decision == true
}

test_allow_simulation_eudi_issuer if {
	ctx := object.union(_base_ctx, {"subject": {"type": "org", "id": "0000009961EUDIISS000"}})
	result := lib.evaluate(eud0002.spec, ctx)
	result.decision == true
}

# ── Scope-authorization ─────────────────────────────────────────────────

test_deny_scope_not_in_allowed_scopes if {
	ctx := object.union(_base_ctx, {"resource": {"scope": "brp:akte:geboorte"}})
	result := lib.evaluate(eud0002.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "SCOPE_NOT_ALLOWED"
}

# The BD scopes must not open the BRP path — the two verticals share the
# actor-whitelist but not the scope-whitelist.
test_deny_bd_scope_on_brp_rule if {
	ctx := object.union(_base_ctx, {"resource": {"scope": "bd:ib:2025"}})
	result := lib.evaluate(eud0002.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "SCOPE_NOT_ALLOWED"
}

test_deny_scope_empty if {
	ctx := object.union(_base_ctx, {"resource": {"scope": ""}})
	result := lib.evaluate(eud0002.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "SCOPE_NOT_ALLOWED"
}

# ── Actor-authorization ─────────────────────────────────────────────────

test_deny_actor_not_in_allowed_actors if {
	ctx := object.union(_base_ctx, {"subject": {"type": "org", "id": "00000001234567890000"}})
	result := lib.evaluate(eud0002.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "ACTOR_NOT_ALLOWED"
}

# ── PID ─────────────────────────────────────────────────────────────────

test_deny_pid_missing if {
	ctx := object.union(_base_ctx, {"pip": {"pid": {"pi": ""}}})
	result := lib.evaluate(eud0002.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "PID_NOT_PRESENT"
}

# ── Year-axis is off ────────────────────────────────────────────────────
# The BRP query carries no belastingjaren; with years_in_scopes on, the
# lib's third clause ("no requested years") would fail closed. This test
# pins that the axis stays declared-off for this rule.

test_allow_without_any_year_args if {
	ctx := object.union(_base_ctx, {"args": {}})
	result := lib.evaluate(eud0002.spec, ctx)
	result.decision == true
}

test_year_axis_skipped if {
	result := lib.evaluate(eud0002.spec, _base_ctx)
	some step in result.context.steps
	step.code == "YEAR_NOT_COVERED"
	step.status == "skipped"
}

# ── Rule separation ─────────────────────────────────────────────────────
# The two EUDI rules must not cover each other's fields: a BD field must
# never be released under the akte-scope, and vice versa.

test_covers_fields_disjoint_from_eud0001 if {
	count(eud0001.covers_fields & eud0002.covers_fields) == 1
	"Query.ingeschrevenPersoon" in (eud0001.covers_fields & eud0002.covers_fields)
}

test_scopes_disjoint_from_eud0001 if {
	count(eud0001.allowed_scopes & eud0002.allowed_scopes) == 0
}
