package dvtp.gbo_test

import data.dvtp.gbo

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
	"pi": "PI-abc123",
	"dienstverlener_oin": _lvg_ir,
}

_lvg_fields := [
	{"id": "vbo", "parent": "Query", "name": "vbo", "scalar": false},
	{"id": "vbo.vboId", "parent": "Verblijfsobject", "name": "vboId", "scalar": true},
]

_lvg_input(actor, scope) := {
	"subject": {"type": "org", "id": actor},
	"context": {
		"time": "2026-09-22T12:00:00Z",
		"resource": {"scope": scope},
		"resolved": {
			"fields": _lvg_fields,
			"args": {"bsn": "PI-abc123", "vboId": "0632010000099412"},
		},
	},
}

test_engine_allows_the_ownership_check_via_lvg0001 if {
	result := gbo.response with input as _lvg_input(_lvg_ir, "lvg:vbo:eigendom")
		with data.dvtp.gbo.consent.resolved as _lvg_consent
	result.decision == true
	result.context.granted[0].rule == "LVG0001"
}

test_engine_denies_a_bd_consent_on_lvg_fields if {
	result := gbo.response with input as _lvg_input(_lvg_hv, "bd:ib:2025")
		with data.dvtp.gbo.consent.resolved as object.union(_lvg_consent, {"granted_scopes": ["bd:ib:2025"], "dienstverlener_oin": _lvg_hv})
	result.decision == false
	result.context.reason_admin.code == "SCOPE_NOT_ALLOWED"
}

test_engine_denies_an_lvg_consent_on_bd_fields if {
	# The other direction: the ownership consent does not open income data.
	result := gbo.response with input as {
		"subject": {"type": "org", "id": _lvg_ir},
		"context": {
			"time": "2026-09-22T12:00:00Z",
			"resource": {"scope": "lvg:vbo:eigendom"},
			"resolved": {
				"fields": [{"id": "aangifte.box1", "parent": "AangifteIH", "name": "box1Inkomen", "scalar": false}],
				"args": {"bsn": "PI-abc123", "belastingjaren.0": "2025"},
			},
		},
	}
		with data.dvtp.gbo.consent.resolved as _lvg_consent
	result.decision == false
}
