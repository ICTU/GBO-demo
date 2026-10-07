package dvtp.gbo.rules.eud0002_test

import data.dvtp.gbo.fixtures_test as fx
import data.dvtp.gbo.lib
import data.dvtp.gbo.rules.eud0001
import data.dvtp.gbo.rules.eud0002

# ═══════════════════════════════════════════════════════════════════════════
# EUD0002 checks (actor, PID; no scope or year), through lib.evaluate with
# the rule's own spec, apart from the engine.
# ═══════════════════════════════════════════════════════════════════════════

_base_ctx := {
	"subject": {"type": "org", "id": "00000004000000004000"},
	"time": "2026-07-27T12:00:00Z",
	"resource": {"scope": ""},
	"pip": {},
	"field": fx.death_certificate_fields("999991772")[0],
}

_on(field) := object.union(object.remove(_base_ctx, ["field"]), {"field": field})

# ── Happy path ──────────────────────────────────────────────────────────

test_allow_valid_actor_pid if {
	result := lib.evaluate(eud0002.spec, _base_ctx)
	result.decision == true
}

test_allow_simulation_eudi_issuer if {
	ctx := object.union(_base_ctx, {"subject": {"type": "org", "id": "0000009961MINEZK0000"}})
	result := lib.evaluate(eud0002.spec, ctx)
	result.decision == true
}

# ── Actor ───────────────────────────────────────────────────────────────

test_deny_actor_not_in_allowed_actors if {
	ctx := object.union(_base_ctx, {"subject": {"type": "org", "id": "00000001234567890000"}})
	result := lib.evaluate(eud0002.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "ACTOR_NOT_ALLOWED"
}

# ── PID ─────────────────────────────────────────────────────────────────

test_deny_pid_missing if {
	result := lib.evaluate(eud0002.spec, _on(fx.death_certificate_fields("")[0]))
	result.decision == false
	result.context.reason_admin.code == "PID_NOT_PRESENT"
}

# ── Year check is off ───────────────────────────────────────────────────
# The BRP query has no year filter, so a year check would fail closed.

test_allow_on_a_leaf_of_the_certificate if {
	result := lib.evaluate(eud0002.spec, _on(fx.death_certificate_fields("999991772")[1]))
	result.decision == true
}

test_year_axis_skipped if {
	result := lib.evaluate(eud0002.spec, _base_ctx)
	some step in result.context.steps
	step.code == "YEAR_NOT_COVERED"
	step.status == "skipped"
}

# ── Rule separation ─────────────────────────────────────────────────────
# Neither EUDI rule may release the other's fields.

test_covers_fields_disjoint_from_eud0001 if {
	count(eud0001.covers_fields & eud0002.covers_fields) == 0
}

test_income_rule_has_no_catalog_scope if {
	not object.get(eud0001.spec, "allowed_scopes", false)
}

test_brp_rule_has_no_catalog_scope if {
	not object.get(eud0002.spec, "allowed_scopes", false)
}
