package dvtp.gbo.rules.lvg0001_test

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
	"pi": "PI-abc123",
	"dienstverlener_oin": _ir,
}

_base_ctx := {
	"subject": {"type": "org", "id": _ir},
	"args": {"bsn": "PI-abc123", "vboId": "0632010000099412"},
	"time": "2026-09-22T12:00:00Z",
	"resource": {"scope": "lvg:vbo:eigendom", "pi": "PI-abc123"},
	"pip": {"consent": _consent},
	"field": "Query.vbo",
}

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
	ctx := object.union(_base_ctx, {"args": {"bsn": "PI-someone-else", "vboId": "0632010000099412"}})
	result := lib.evaluate(lvg0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "CONSTRAINT_MISMATCH"
}

test_deny_withdrawn_consent if {
	ctx := object.union(_base_ctx, {"pip": {"consent": object.union(_consent, {"withdrawn": true})}})
	result := lib.evaluate(lvg0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "CONSENT_WITHDRAWN"
}
