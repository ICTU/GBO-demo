package gbo.rules.eudi.eud0002_test

import data.gbo.fixtures_test as fx
import data.gbo.rules.eudi.eud0001
import data.gbo.rules.eudi.eud0002

# ═══════════════════════════════════════════════════════════════════════════
# EUD0002 on its own (actor, PID; no scope or year), apart from the engine.
# The subject is on the root field, position 0.
# ═══════════════════════════════════════════════════════════════════════════

_issuer := "00000004000000004000"

_request(actor, bsn) := fx.request(actor, "brp", fx.death_certificate_fields(bsn), {})

_reasons(req) := r if {
	r := eud0002.deny | {x | some reasons in eud0002.deny_field; some x in reasons} with input as req
}

_reasons_on(req, i) := r if {
	r := eud0002.deny | object.get(eud0002.deny_field, i, set()) with input as req
}

# ── Allowed ─────────────────────────────────────────────────────────────

test_allow_valid_actor_pid if {
	_reasons(_request(_issuer, "999991772")) == set()
}

test_allow_simulation_eudi_issuer if {
	_reasons(_request("0000009961MINEZK0000", "999991772")) == set()
}

# ── Actor ───────────────────────────────────────────────────────────────

test_deny_actor_not_in_allowed_actors if {
	_reasons(_request("00000001234567890000", "999991772")) == {"ACTOR_NOT_ALLOWED"}
}

# ── PID ─────────────────────────────────────────────────────────────────

test_deny_pid_missing if {
	_reasons_on(_request(_issuer, ""), 0) == {"PID_NOT_PRESENT"}
}

test_allow_on_a_leaf_of_the_certificate if {
	_reasons_on(_request(_issuer, "999991772"), 1) == set()
}

# ── Rule separation ─────────────────────────────────────────────────────
# Neither EUDI rule may release the other's fields.

test_covers_fields_disjoint_from_eud0001 if {
	count(eud0001.covers_fields & eud0002.covers_fields) == 0
}
