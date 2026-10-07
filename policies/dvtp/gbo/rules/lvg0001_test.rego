package dvtp.gbo.rules.lvg0001_test

import data.dvtp.gbo.fixtures_test as fx
import data.dvtp.gbo.lib
import data.dvtp.gbo.rules.lvg0001

# ═══════════════════════════════════════════════════════════════════════════
# LVG0001 — the ownership check of the LVG/Installatie Register pilot.
#
# The axes run against lib.evaluate(spec, ctx). The engine cases, which bind
# the rule through its covered fields, live in lvg_engine_test.rego: a test
# package under rules/ would be walked by the engine itself.
# ═══════════════════════════════════════════════════════════════════════════

_ir := "99999999900000001000"

_hv := "99999999900000000300"

_consent := {
	"context_valid": true,
	"status_available": true,
	"exists": true,
	"withdrawn": false,
	"valid_until": "2030-01-01T00:00:00Z",
	"granted_scopes": ["lvg:vbo:eigendom"],
	"dienstverlener_oin": _ir,
}

_base_ctx := {
	"subject": {"type": "org", "id": _ir},
	"time": "2026-09-22T12:00:00Z",
	"resource": {"scope": "lvg:vbo:eigendom", "subject_placeholders": {"consent:identity", "consent:pseudonym"}},
	"pip": {"consent": _consent},
	"field": fx.vbo("consent:identity"),
}

_on(field) := object.union(object.remove(_base_ctx, ["field"]), {"field": field})

test_allow_consented_ownership_check if {
	result := lib.evaluate(lvg0001.spec, _base_ctx)
	result.decision == true
}

test_deny_scope_other_than_lvg if {
	# A consent for BD income data, sent with its own scope: it covers that
	# scope, so only the rule's scope pin stops it on LVG fields.
	ctx := object.union(_base_ctx, {
		"resource": object.union(_base_ctx.resource, {"scope": "bd:ib:2025"}),
		"pip": {"consent": object.union(_consent, {"granted_scopes": ["bd:ib:2025"]})},
	})
	result := lib.evaluate(lvg0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "SCOPE_NOT_ALLOWED"
}

test_deny_other_consumer_with_the_ir_consent if {
	ctx := object.union(_base_ctx, {"subject": {"type": "org", "id": _hv}})
	result := lib.evaluate(lvg0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "CONSENT_ACTOR_MISMATCH"
}

test_deny_question_about_another_citizen if {
	result := lib.evaluate(lvg0001.spec, _on(fx.vbo("999991772")))
	result.decision == false
	result.context.reason_admin.code == "CONSTRAINT_MISMATCH"
}

# LVG's API takes a BSN, not a pseudonym.
test_deny_pseudonym_placeholder if {
	result := lib.evaluate(lvg0001.spec, _on(fx.vbo("consent:pseudonym")))
	result.decision == false
	result.context.reason_admin.code == "CONSTRAINT_MISMATCH"
}

test_deny_withdrawn_consent if {
	ctx := object.union(_base_ctx, {"pip": {"consent": object.union(_consent, {"withdrawn": true})}})
	result := lib.evaluate(lvg0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "CONSENT_WITHDRAWN"
}
