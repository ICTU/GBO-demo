package dvtp.gbo_test

import data.dvtp.gbo

# Engine-level ctx-shape: the request-mapper places the consent it
# fetched in input.context.pip.consent; the engine mirrors its pi onto
# ctx.resource for the constraint-binding rule.

_pip := {"consent": {"context_valid": true, "status_available": true, "exists": true, "withdrawn": false, "granted_scopes": ["bd:ib:2025"], "valid_until": "2030-01-01T00:00:00Z", "pi": "PI-abc123", "dienstverlener_oin": "peer-oin-123"}}

_input := {
	"subject": {"type": "org", "id": "peer-oin-123"},
	"context": {"resource": {"variables": {"bsn": "PI-abc123"}}, "pip": _pip},
}

test_ctx_pip_passthrough if {
	ctx := gbo._ctx with input as _input
	ctx.pip.consent.exists == true
	ctx.pip.consent.pi == "PI-abc123"
}

test_ctx_resource_pi_mirror if {
	ctx := gbo._ctx with input as _input
	ctx.resource.pi == "PI-abc123"
}

test_ctx_resource_pi_empty_without_consent if {
	ctx := gbo._ctx with input as {"subject": {"type": "org", "id": "x"}, "context": {}}
	ctx.resource.pi == ""
}

# A PID-regime request: no consent token, so no pip.consent, and a plain
# BSN in the source-declared subject variable, as the EUDI adapter sends it.
_eudi_context(fields, args) := {"resolved": {
	"fields": fields,
	"args": object.union(args, {"vars.bsn": "999991772"}),
}}

test_pid_evidence_selects_income_rule_by_fields if {
	ctx := _eudi_context(
		[{"id": "income.box1", "parent": "AangifteIH", "name": "box1Inkomen", "scalar": false}],
		{"belastingjaren.0": "2024"},
	)
	result := gbo.response with input as {
		"subject": {"type": "org", "id": "99999999900000000100"},
		"context": ctx,
	}
	result.decision == true
	result.context.granted[0].rule == "EUD0001"
}

test_pid_evidence_selects_brp_rule_by_fields if {
	ctx := _eudi_context(
		[{"id": "brp.verklaring", "parent": "AkteVanOverlijden", "name": "verklaring_tekst", "scalar": true}],
		{},
	)
	result := gbo.response with input as {
		"subject": {"type": "org", "id": "99999999900000000100"},
		"context": ctx,
	}
	result.decision == true
	result.context.granted[0].rule == "EUD0002"
}

# ── Deny-reason surfacing for consent-based requests ─────────────────────
# The gap #334 step 1 closed: nothing in the suite asserted an engine-level
# deny reason, so no test could see what the flow dispatch had been protecting.
# box1Inkomen is covered by BOTH DVT0001 and EUD0001. A consent-based
# request carries no PID, so EUD0001 is evaluated here and denies with
# PID_NOT_PRESENT (priority 55). On priority alone that would out-rank the
# genuine DvTP reason (CONSENT_SCOPE_MISMATCH 40, YEAR_NOT_COVERED 41,
# CONSTRAINT_MISMATCH 30); _best_reason ranks by cascade depth first.

_consent := {
	"context_valid": true,
	"status_available": true,
	"exists": true,
	"withdrawn": false,
	"valid_until": "2030-01-01T00:00:00Z",
	"granted_scopes": ["bd:ib:2025"],
	"pi": "PI-abc123",
	"dienstverlener_oin": "99999999900000000300",
}

# A consent-carrying request for a field on the DVT0001 ∩ EUD0001 overlap.
# The consent itself is valid throughout — each case fails on exactly one
# DvTP axis, so the asserted code is the genuine reason and nothing else.
_dvtp_input(scope, args) := {
	"subject": {"type": "org", "id": "99999999900000000300"},
	"context": {
		"time": "2026-07-06T12:00:00Z",
		"resource": {"scope": scope},
		"pip": {"consent": _consent},
		"resolved": {
			"fields": [{
				"id": "aangifte.box1",
				"parent": "AangifteIH",
				"name": "box1Inkomen",
				"scalar": false,
			}],
			"args": args,
		},
	},
}

test_dvtp_deny_surfaces_year_not_covered if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", {"bsn": "PI-abc123", "belastingjaren.0": "2024"})
	result.decision == false
	result.context.reason_admin.code == "YEAR_NOT_COVERED"
}

test_dvtp_deny_surfaces_consent_scope_mismatch if {
	result := gbo.response with input as _dvtp_input("bd:ib:2023", {"bsn": "PI-abc123", "belastingjaren.0": "2025"})
	result.decision == false
	result.context.reason_admin.code == "CONSENT_SCOPE_MISMATCH"
}

test_dvtp_deny_surfaces_constraint_mismatch if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", {"bsn": "PI-other", "belastingjaren.0": "2025"})
	result.decision == false
	result.context.reason_admin.code == "CONSTRAINT_MISMATCH"
}

# ── Deny-reason surfacing for PID-based requests ─────────────────────────
# The mirror of the cases above, and the sharper half of the problem. A
# PID-based request carries no consent, so DVT0001 fails on its FIRST axis
# with CONSENT_CONTEXT_INVALID (priority 70), the top of the whole table.
# On priority alone it would out-rank every genuine EUDI reason, not just
# the low-priority ones.

# A PID-regime request for the same DVT0001 ∩ EUD0001 field: no consent
# token, and a plain BSN in the subject variable.
_eudi_input(actor, year) := {
	"subject": {"type": "org", "id": actor},
	"context": {
		"time": "2026-07-06T12:00:00Z",
		"resolved": {
			"fields": [{
				"id": "aangifte.box1",
				"parent": "AangifteIH",
				"name": "box1Inkomen",
				"scalar": false,
			}],
			"args": {"vars.bsn": "999991772", "belastingjaren.0": year},
		},
	},
}

test_eudi_deny_surfaces_year_not_allowed if {
	result := gbo.response with input as _eudi_input("99999999900000000100", "2023")
	result.decision == false
	result.context.reason_admin.code == "YEAR_NOT_ALLOWED"
}

test_eudi_deny_surfaces_actor_not_allowed if {
	result := gbo.response with input as _eudi_input("99999999900000000999", "2024")
	result.decision == false
	result.context.reason_admin.code == "ACTOR_NOT_ALLOWED"
}

# ── Evidence decides the regime ──────────────────────────────────────────
# With the flow dispatch gone, every rule covering the field is evaluated
# on every request. A consent token selects the consent regime; its
# absence, the PID regime. There is no third case: "both" cannot occur
# when one regime is defined as the absence of the other.

test_dvtp_allow_grants_via_consent_rule if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", {"bsn": "PI-abc123", "belastingjaren.0": "2025"})
	result.decision == true
	result.context.granted[0].rule == "DVT0001"
}

# No consent token, so the request is judged under the PID regime. A
# consent-based consumer that omits its token therefore meets the EUDI
# rules' allowed_actors, not its consent — which is why that whitelist must
# stay disjoint from the consent-based consumers (test below).
test_no_consent_token_puts_the_request_under_the_pid_regime if {
	result := gbo.response with input as {
		"subject": {"type": "org", "id": "99999999900000000300"},
		"context": {
			"time": "2026-07-06T12:00:00Z",
			"pip": {},
			"resolved": {
				"fields": [{"id": "aangifte.box1", "parent": "AangifteIH", "name": "box1Inkomen", "scalar": false}],
				"args": {"bsn": "PI-abc123", "vars.bsn": "PI-abc123", "belastingjaren.0": "2025"},
			},
		},
	}
	result.decision == false
	result.context.reason_admin.code == "ACTOR_NOT_ALLOWED"
}

# ── The invariant the PID regime now rests on ────────────────────────────
# With `flow` gone, the request-mapper selects the consent regime on a
# verified consent token and falls through to the PID regime otherwise.
# Absence of a header is therefore what puts a request under the EUDI
# rules, and the only gate left on them is each rule's own allowed_actors.
#
# That is safe exactly as long as no OIN is BOTH a designated EDI-issuer
# and a consent-based consumer. Such an OIN could skip the citizen's
# consent — and with it consent_must_cover_scope and years_in_scopes —
# simply by omitting the consent token, and be judged instead under the
# rule's own allowed_years. This test is the enforcement of that
# invariant; it is a deployment fact, so it pins the seeded consumer
# rather than deriving anything.
#
# If it fails: do NOT widen the test. Either that OIN must not hold a
# DvTP contract, or the PID regime needs positive evidence of its own
# (see #334 — a verified PID assertion would remove the fall-through).
_dvtp_consumer_oins := {"99999999900000000300"} # HV, seed-bri-connection-hv.sh

test_pid_rule_actors_are_disjoint_from_consent_consumers if {
	every rule_id in {"EUD0001", "EUD0002"} {
		actors := object.get(gbo._rule_meta[rule_id].spec, "allowed_actors", set())
		count(actors) > 0
		count(actors & _dvtp_consumer_oins) == 0
	}
}

# ── A depth tie is broken by the regime the request is under ─────────────
# When every rule passes nothing, cascade depth cannot separate them. The
# regime can: pip.consent is present exactly when the request carried a
# consent token — verified or not — and absent in the PID regime. Review of
# #363: a PID-regime request whose evidence failed surfaced
# CONSENT_CONTEXT_INVALID, where main said PID_NOT_PRESENT.

_box1 := [{"id": "aangifte.box1", "parent": "AangifteIH", "name": "box1Inkomen", "scalar": false}]

test_pid_regime_without_a_subject_surfaces_pid_not_present if {
	result := gbo.response with input as {
		"subject": {"type": "org", "id": "99999999900000000100"},
		"context": {
			"time": "2026-07-06T12:00:00Z",
			"resolved": {"fields": _box1, "args": {"belastingjaren.0": "2024"}},
		},
	}
	result.decision == false
	result.context.reason_admin.code == "PID_NOT_PRESENT"
}

test_unverifiable_consent_surfaces_consent_context_invalid if {
	result := gbo.response with input as {
		"subject": {"type": "org", "id": "99999999900000000300"},
		"context": {
			"time": "2026-07-06T12:00:00Z",
			"pip": {"consent": {"context_valid": false, "status_available": false, "exists": false}},
			"resolved": {"fields": _box1, "args": {"bsn": "", "belastingjaren.0": "2025"}},
		},
	}
	result.decision == false
	result.context.reason_admin.code == "CONSENT_CONTEXT_INVALID"
}
