package dvtp.gbo_test

import data.dvtp.gbo

# Engine-level ctx-shape. The consent is resolved by the policy itself
# (data.dvtp.gbo.consent, covered end to end in consent_test.rego); these
# tests stand a resolved consent in for it with `with`, so they exercise the
# engine alone. The engine mirrors the consent's pi onto ctx.resource for
# the constraint-binding rule.

_pip_consent := {"context_valid": true, "status_available": true, "exists": true, "withdrawn": false, "granted_scopes": ["bd:ib:2025"], "valid_until": "2030-01-01T00:00:00Z", "pi": "PI-abc123", "dienstverlener_oin": "peer-oin-123"}

_input := {
	"subject": {"type": "org", "id": "peer-oin-123"},
	"context": {"resource": {"variables": {"bsn": "PI-abc123"}}},
}

test_ctx_pip_carries_the_resolved_consent if {
	ctx := gbo._ctx with input as _input with data.dvtp.gbo.consent.resolved as _pip_consent
	ctx.pip.consent.exists == true
	ctx.pip.consent.pi == "PI-abc123"
}

test_ctx_resource_pi_mirror if {
	ctx := gbo._ctx with input as _input with data.dvtp.gbo.consent.resolved as _pip_consent
	ctx.resource.pi == "PI-abc123"
}

test_ctx_resource_pi_empty_without_consent if {
	ctx := gbo._ctx with input as {"subject": {"type": "org", "id": "x"}, "context": {}}
	ctx.resource.pi == ""
}

# A consent in input is not a consent the policy verified. Nothing upstream
# is meant to set pip.consent any more; if something does, it is dropped
# rather than decided on.
test_input_pip_consent_is_not_trusted if {
	req := object.union(
		_dvtp_input("bd:ib:2025", {"bsn": "PI-abc123", "belastingjaren.0": "2025"}),
		{"context": {"pip": {"consent": _consent}}},
	)
	ctx := gbo._ctx with input as req
	not ctx.pip.consent
	result := gbo.response with input as req
	result.decision == false
	result.context.reason_admin.code == "CONSENT_CONTEXT_INVALID"
}

_eudi_context(fields, args) := {
	"pip": {"pid": {"pi": "PI-2f1a7c9b40e6d853"}},
	"resolved": {"fields": fields, "args": args},
}

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

# Without a consent token there is no consent to report, and the response
# document says nothing about one.
test_response_without_consent_carries_none if {
	result := gbo.response with input as {
		"subject": {"type": "org", "id": "99999999900000000100"},
		"context": _eudi_context(
			[{"id": "income.box1", "parent": "AangifteIH", "name": "box1Inkomen", "scalar": false}],
			{"belastingjaren.0": "2024"},
		),
	}
	not result.context.pip
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
# The consent is supplied per test, as the resolved one.
_dvtp_input(scope, args) := {
	"subject": {"type": "org", "id": "99999999900000000300"},
	"context": {
		"time": "2026-07-06T12:00:00Z",
		"resource": {"scope": scope},
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
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	result.context.reason_admin.code == "YEAR_NOT_COVERED"
}

test_dvtp_deny_surfaces_consent_scope_mismatch if {
	result := gbo.response with input as _dvtp_input("bd:ib:2023", {"bsn": "PI-abc123", "belastingjaren.0": "2025"})
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	result.context.reason_admin.code == "CONSENT_SCOPE_MISMATCH"
}

test_dvtp_deny_surfaces_constraint_mismatch if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", {"bsn": "PI-other", "belastingjaren.0": "2025"})
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	result.context.reason_admin.code == "CONSTRAINT_MISMATCH"
}

# ── Deny-reason surfacing for PID-based requests ─────────────────────────
# The mirror of the cases above, and the sharper half of the problem. A
# PID-based request carries no consent, so DVT0001 fails on its FIRST axis
# with CONSENT_CONTEXT_INVALID (priority 70), the top of the whole table.
# On priority alone it would out-rank every genuine EUDI reason, not just
# the low-priority ones.

# A PID-carrying request for the same DVT0001 ∩ EUD0001 field: a
# well-formed PID and no consent anywhere in the PIP.
_eudi_input(actor, year) := {
	"subject": {"type": "org", "id": actor},
	"context": {
		"time": "2026-07-06T12:00:00Z",
		"pip": {"pid": {"pi": "PI-2f1a7c9b40e6d853"}},
		"resolved": {
			"fields": [{
				"id": "aangifte.box1",
				"parent": "AangifteIH",
				"name": "box1Inkomen",
				"scalar": false,
			}],
			"args": {"belastingjaren.0": year},
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

# ── Evidence decides the regime, and ambiguity is not resolved silently ──
# With the flow dispatch gone, every rule covering the field is evaluated
# on every request. These pin the three outcomes that follow from the
# evidence alone: it fits one rule, it fits none, or it fits both.

test_dvtp_allow_grants_via_consent_rule if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", {"bsn": "PI-abc123", "belastingjaren.0": "2025"})
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == true
	result.context.granted[0].rule == "DVT0001"
}

# The decision log records the consent the decision was taken on: it is not
# in input, so the response document carries it.
test_dvtp_response_carries_the_consent if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", {"bsn": "PI-abc123", "belastingjaren.0": "2025"})
		with data.dvtp.gbo.consent.resolved as _consent
	result.context.pip.consent == _consent
}

# Neither a consent nor a PID, and no enrichment attempted either. Every
# rule fails its own basis check, so neither cascade depth nor the attempted
# regime separates them, and the priority table decides.
test_no_evidence_denies if {
	result := gbo.response with input as {
		"subject": {"type": "org", "id": "99999999900000000300"},
		"context": {
			"time": "2026-07-06T12:00:00Z",
			"pip": {},
			"resolved": {
				"fields": [{"id": "aangifte.box1", "parent": "AangifteIH", "name": "box1Inkomen", "scalar": false}],
				"args": {"bsn": "PI-abc123", "belastingjaren.0": "2025"},
			},
		},
	}
	result.decision == false
	result.context.reason_admin.code == "CONSENT_CONTEXT_INVALID"
}

# Both a verified consent and a disclosed PID. Both DVT0001 and EUD0001
# would grant; the engine must not pick one by rule ordering.
test_both_evidence_denies_rather_than_picking_a_regime if {
	result := gbo.response with input as {
		"subject": {"type": "org", "id": "99999999900000000100"},
		"context": {
			"time": "2026-07-06T12:00:00Z",
			"resource": {"scope": "bd:ib:2025"},
			"pip": {"pid": {"pi": "PI-2f1a7c9b40e6d853"}},
			"resolved": {
				"fields": [{"id": "aangifte.box1", "parent": "AangifteIH", "name": "box1Inkomen", "scalar": false}],
				"args": {"bsn": "PI-abc123", "belastingjaren.0": "2025"},
			},
		},
	}
		with data.dvtp.gbo.consent.resolved as object.union(_consent, {"dienstverlener_oin": "99999999900000000100"})
	result.decision == false
	result.context.reason_admin.code == "AMBIGUOUS_EVIDENCE"
}

# ── The invariant the PID regime now rests on ────────────────────────────
# With `flow` gone, a consent token selects the consent regime and its
# absence the PID regime. Absence of a header is therefore what puts a
# request under the EUDI rules, and the only gate left on them is each
# rule's own allowed_actors.
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

# ── A failed enrichment keeps the regime it attempted ────────────────────
# pip.consent is resolved whenever the request carries a consent token, and
# the request-mapper fills pip.pid otherwise — also when that attempt fails:
# an unverifiable token, or a BSNk error that leaves pi empty. Every rule
# then passes nothing, so cascade depth cannot separate them; the attempt
# can. Review of #363: a PID-based request whose BSN could not be
# pseudonymised surfaced CONSENT_CONTEXT_INVALID, where main said
# PID_NOT_PRESENT.

_box1 := [{"id": "aangifte.box1", "parent": "AangifteIH", "name": "box1Inkomen", "scalar": false}]

test_failed_pid_enrichment_surfaces_pid_not_present if {
	result := gbo.response with input as {
		"subject": {"type": "org", "id": "99999999900000000100"},
		"context": {
			"time": "2026-07-06T12:00:00Z",
			"pip": {"pid": {"pi": ""}},
			"resolved": {"fields": _box1, "args": {"belastingjaren.0": "2024"}},
		},
	}
	result.decision == false
	result.context.reason_admin.code == "PID_NOT_PRESENT"
}

_unverifiable_input := {
	"subject": {"type": "org", "id": "99999999900000000300"},
	"context": {
		"time": "2026-07-06T12:00:00Z",
		"resolved": {"fields": _box1, "args": {"bsn": "", "belastingjaren.0": "2025"}},
	},
}

test_unverifiable_consent_surfaces_consent_context_invalid if {
	result := gbo.response with input as _unverifiable_input
		with data.dvtp.gbo.consent.resolved as {"context_valid": false, "status_available": false, "exists": false}
	result.decision == false
	result.context.reason_admin.code == "CONSENT_CONTEXT_INVALID"
}

# The consent PIP can say which check failed; the engine reports that, not
# the generic code.
test_unverifiable_consent_surfaces_its_invalid_code if {
	result := gbo.response with input as _unverifiable_input
		with data.dvtp.gbo.consent.resolved as {
			"context_valid": false,
			"status_available": false,
			"exists": false,
			"invalid_code": "CONSENT_SIGNATURE_INVALID",
		}
	result.decision == false
	result.context.reason_admin.code == "CONSENT_SIGNATURE_INVALID"
}
