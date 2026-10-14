package gbo.rules.asi.asi0001_test

import data.gbo.fixtures_test as fx
import data.gbo.rules.asi.asi0001

# ═══════════════════════════════════════════════════════════════════════════
# ASI0001 on its own (actor, PID; no scope or year), apart from the engine.
# The subject is on the root field, position 0 of fx.personal_fields.
# ═══════════════════════════════════════════════════════════════════════════

_request(actor, bsn) := fx.request(actor, "brp", fx.personal_fields(bsn), {})

_reasons(req) := r if {
	r := asi0001.deny | {x | some reasons in asi0001.deny_field; some x in reasons} with input as req
}

_reasons_on(req, i) := r if {
	r := asi0001.deny | object.get(asi0001.deny_field, i, set()) with input as req
}

# ── Allowed ─────────────────────────────────────────────────────────────

test_allow_the_asip_with_a_bsn if {
	_reasons(_request(fx.asip, "999991772")) == set()
}

# ── Actor ───────────────────────────────────────────────────────────────

test_deny_other_actors if {
	every actor in [fx.eudi_issuer, fx.hv, "00000001234567890000"] {
		_reasons(_request(actor, "999991772")) == {"ACTOR_NOT_ALLOWED"}
	}
}

# ── Regime ──────────────────────────────────────────────────────────────

test_deny_with_a_consent_token if {
	r := _reasons(_request(fx.asip, "999991772")) with data.gbo.consent.resolved as {"id": "c-1"}
	r == {"PID_NOT_PRESENT"}
}

# ── PID ─────────────────────────────────────────────────────────────────

test_deny_without_a_citizen if {
	every bsn in ["", "consent:identity", "consent:pseudonym"] {
		_reasons_on(_request(fx.asip, bsn), 0) == {"PID_NOT_PRESENT"}
	}
}

test_allow_on_a_leaf_of_the_person if {
	_reasons_on(_request(fx.asip, "999991772"), 1) == set()
}

# ── Scope of the rule ───────────────────────────────────────────────────

test_judges_brp_only if {
	asi0001.services == {"brp"}
}

# Marriage, authority and a non-resident's address are not the ASIP's.
test_covers_no_other_brp_fields if {
	not "IngeschrevenPersoon.heeftHuwelijk" in asi0001.covers_fields
	not "IngeschrevenPersoon.gezag" in asi0001.covers_fields
	not "NietIngezetene.woontOp" in asi0001.covers_fields
	count(asi0001.covers_types) == 0
}
