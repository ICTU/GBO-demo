package dvtp.gbo_test

import data.dvtp.gbo
import data.dvtp.gbo.fixtures_test as fx

# LVG0001 through the engine: the ownership-check fields of the LVG source
# bind to it, and the two consent regimes do not open each other's fields.

_lvg_ir := "99999999900000001000"

_lvg_hv := "99999999900000000300"

_lvg_consent := {
	"context_valid": true,
	"status_available": true,
	"exists": true,
	"withdrawn": false,
	"valid_until": "2030-01-01T00:00:00Z",
	"granted_scopes": ["lvg:vbo:eigendom"],
	"dienstverlener_oin": _lvg_ir,
}

_lvg_input(actor, scope) := fx.request(actor, "lvg", fx.ownership_fields("consent:identity"), fx.scope_headers(scope))

test_engine_allows_the_ownership_check_via_lvg0001 if {
	result := gbo.response with input as _lvg_input(_lvg_ir, "lvg:vbo:eigendom")
		with data.dvtp.gbo.consent.resolved as _lvg_consent
	result.decision == true
	every g in result.context.granted {
		g.rule == "LVG0001"
	}
}

test_engine_denies_a_question_about_another_citizen if {
	req := fx.request(_lvg_ir, "lvg", fx.ownership_fields("999991772"), fx.scope_headers("lvg:vbo:eigendom"))
	result := gbo.response with input as req with data.dvtp.gbo.consent.resolved as _lvg_consent
	result.decision == false
	result.context.reason_admin.code == "CONSTRAINT_MISMATCH"
}

test_engine_denies_a_bd_consent_on_lvg_fields if {
	result := gbo.response with input as _lvg_input(_lvg_hv, "bd:ib:2025")
		with data.dvtp.gbo.consent.resolved as object.union(_lvg_consent, {"granted_scopes": ["bd:ib:2025"], "dienstverlener_oin": _lvg_hv})
	result.decision == false
	result.context.reason_admin.code == "SCOPE_NOT_ALLOWED"
}

test_engine_denies_an_lvg_consent_on_bd_fields if {
	# The other direction: the ownership consent does not open income data.
	result := gbo.response with input as fx.income(_lvg_ir, "consent:identity", [2025], fx.scope_headers("lvg:vbo:eigendom"))
		with data.dvtp.gbo.consent.resolved as _lvg_consent
	result.decision == false
}
