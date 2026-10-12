package gbo.rules.dvtp.lvg0001_test

import data.gbo.fixtures_test as fx
import data.gbo.rules.dvtp.lvg0001

# ═══════════════════════════════════════════════════════════════════════════
# LVG0001 on its own: a request goes in, the rule's reasons come out. The
# engine cases live in lvg_engine_test.rego.
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

_request(actor, bsn, scope) := fx.request(actor, "lvg", fx.ownership_fields(bsn), fx.scope_headers(scope))

_ownership := _request(_ir, "consent:identity", "lvg:vbo:eigendom")

_reasons(req, consent) := r if {
	r := lvg0001.deny | {x | some reasons in lvg0001.deny_field; some x in reasons} with input as req
		with data.gbo.consent.resolved as consent
}

test_allow_consented_ownership_check if {
	_reasons(_ownership, _consent) == set()
}

test_deny_scope_other_than_lvg if {
	# A BD consent sent with its own scope covers it; only the scope pin
	# stops it on LVG fields.
	req := _request(_ir, "consent:identity", "bd:ib:2025")
	consent := object.union(_consent, {"granted_scopes": ["bd:ib:2025"]})
	_reasons(req, consent) == {"SCOPE_NOT_ALLOWED"}
}

test_deny_other_consumer_with_the_ir_consent if {
	_reasons(_request(_hv, "consent:identity", "lvg:vbo:eigendom"), _consent) == {"CONSENT_ACTOR_MISMATCH"}
}

test_deny_question_about_another_citizen if {
	_reasons(_request(_ir, "999991772", "lvg:vbo:eigendom"), _consent) == {"CONSTRAINT_MISMATCH"}
}

# LVG's API takes a BSN, not a pseudonym.
test_deny_pseudonym_placeholder if {
	_reasons(_request(_ir, "consent:pseudonym", "lvg:vbo:eigendom"), _consent) == {"CONSTRAINT_MISMATCH"}
}

test_deny_withdrawn_consent if {
	_reasons(_ownership, object.union(_consent, {"withdrawn": true})) == {"CONSENT_WITHDRAWN"}
}
