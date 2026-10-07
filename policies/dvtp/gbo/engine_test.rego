package dvtp.gbo_test

import data.dvtp.gbo
import data.dvtp.gbo.fixtures_test as fx

# Engine-level ctx-shape. The consent is resolved by the policy itself
# (data.dvtp.gbo.consent, covered end to end in consent_test.rego); these
# tests stand a resolved consent in for it with `with`, so they exercise the
# engine alone. The engine puts the subject placeholders on ctx.resource for
# the constraint-binding rule.

_pip_consent := {"context_valid": true, "status_available": true, "exists": true, "withdrawn": false, "granted_scopes": ["bd:ib:2025"], "valid_until": "2030-01-01T00:00:00Z", "dienstverlener_oin": "peer-oin-123"}

_input := {"subject": {"type": "org", "id": "peer-oin-123"}, "context": {}}

test_ctx_pip_carries_the_resolved_consent if {
	ctx := gbo._ctx with input as _input with data.dvtp.gbo.consent.resolved as _pip_consent
	ctx.pip.consent.exists == true
}

test_ctx_resource_has_both_placeholders if {
	ctx := gbo._ctx with input as _input with data.dvtp.gbo.consent.resolved as _pip_consent
	ctx.resource.subject_placeholders == {"consent:identity", "consent:pseudonym"}
}

# Without a verified consent the placeholders stand for nobody.
test_ctx_resource_placeholders_empty_without_consent if {
	ctx := gbo._ctx with input as {"subject": {"type": "org", "id": "x"}, "context": {}}
	ctx.resource.subject_placeholders == set()
}

test_ctx_resource_placeholders_empty_for_an_unverified_consent if {
	ctx := gbo._ctx with input as _input with data.dvtp.gbo.consent.resolved as {"context_valid": false, "invalid_code": "CONSENT_SIGNATURE_INVALID"}
	ctx.resource.subject_placeholders == set()
}

# What a consent-based query may name its subject with is the policy's to
# say. A request cannot move it by supplying its own.
test_ctx_resource_placeholders_are_not_taken_from_the_request if {
	req := {"subject": {"type": "org", "id": "x"}, "context": {"resource": {"subject_placeholders": ["999991772"]}}}
	ctx := gbo._ctx with input as req with data.dvtp.gbo.consent.resolved as _pip_consent
	ctx.resource.subject_placeholders == {"consent:identity", "consent:pseudonym"}
}

# The rules see every mapper record (for checks that deny over several
# fields) and the resource without the mapper's output (profile 9.4).
test_ctx_carries_all_fields_and_the_resource_without_the_mapper if {
	typename := {"path": ["ingeschrevenPersoon", "__typename"], "parentType": "IngeschrevenPersoon", "field": "__typename", "leaf": true}
	fields := array.concat(fx.income_fields("consent:identity", fx.literal([2025])), [typename])
	req := object.union(fx.request(fx.hv, "bri", fields, fx.scope_headers("bd:ib:2025")), {"resource": {"attributes": {"path": "/graphql"}}})
	ctx := gbo._ctx with input as req with data.dvtp.gbo.consent.resolved as _pip_consent
	ctx.fields == fields
	ctx.resource.type == "uri"
	ctx.resource.id == "/graphql"
	ctx.resource.attributes == {"path": "/graphql"}
	ctx.resource.scope == "bd:ib:2025"
	ctx.resource.subject_placeholders == {"consent:identity", "consent:pseudonym"}
}

# The scope is the one the consumer declares, in any header-name case.
test_ctx_resource_scope_is_the_declared_header if {
	ctx := gbo._ctx with input as {"subject": {"id": "x"}, "context": {"headers": {"x-gbo-scope": "bd:ib:2025"}}}
	ctx.resource.scope == "bd:ib:2025"
}

# Two values under two spellings are not resolved by picking one.
test_ctx_resource_scope_twice_is_no_scope if {
	ctx := gbo._ctx with input as {"subject": {"id": "x"}, "context": {"headers": {"X-Gbo-Scope": "bd:ib:2025", "x-gbo-scope": "bd:ib:2024"}}}
	ctx.resource.scope == ""
}

# A consent in input is not a consent the policy verified. Nothing upstream
# is meant to set pip.consent any more; if something does, it is dropped
# rather than decided on, and without a consent token the request is judged
# under the PID regime, where its placeholder names nobody.
test_input_pip_consent_is_not_trusted if {
	req := object.union(_dvtp_input("bd:ib:2025", "consent:identity", [2025]), {"context": {"pip": {"consent": _consent}}})
	ctx := gbo._ctx with input as req
	not ctx.pip.consent
	result := gbo.response with input as req
	result.decision == false
	result.context.reason_admin.code == "PID_NOT_PRESENT"
}

# A PID-regime request: no consent token, and a plain BSN in the root
# field's argument, as the EUDI adapter sends it.
test_pid_evidence_selects_income_rule_by_fields if {
	result := gbo.response with input as fx.income(fx.eudi_issuer, "999991772", [2024], {})
	result.decision == true
	result.context.granted[0].rule == "EUD0001"
}

test_pid_evidence_selects_brp_rule_by_fields if {
	result := gbo.response with input as fx.request(fx.eudi_issuer, "brp", fx.death_certificate_fields("999991772"), {})
	result.decision == true
	every g in result.context.granted {
		g.rule == "EUD0002"
	}
}

# Without a consent token there is no consent to report, and the response
# document says nothing about one.
test_response_without_consent_carries_none if {
	result := gbo.response with input as fx.income(fx.eudi_issuer, "999991772", [2024], {})
	not result.context.pip
}

# ── The mapper's output, checked before any rule ─────────────────────────
# Section 9.5 of the FTV GraphQL profile, in its order. Each failure denies
# the request as a whole, whatever the rules would say.

test_mapper_output_missing_denies if {
	req := object.remove(_dvtp_input("bd:ib:2025", "consent:identity", [2025]), ["resource"])
	result := gbo.response with input as req with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	result.context.reason_admin == {"code": "CONFIG_ERROR", "subcode": "MAPPER_OUTPUT_MISSING"}
}

test_mapper_output_of_another_profile_denies if {
	output := object.union(fx.graphql("bri", fx.income_fields("consent:identity", fx.literal([2025]))), {"profile": "ftv-graphql/0.2"})
	result := gbo.response with input as fx.request_with(fx.hv, "bri", output, fx.scope_headers("bd:ib:2025"))
		with data.dvtp.gbo.consent.resolved as _consent
	result.context.reason_admin.subcode == "MAPPER_OUTPUT_MISSING"
}

test_schema_other_than_the_pin_denies if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "consent:identity", [2025])
		with data.dvtp.gbo.consent.resolved as _consent
		with data.dvtp.gbo.graphql_schemas.digests as {"bri": "sha256:other"}
	result.decision == false
	result.context.reason_admin == {"code": "CONFIG_ERROR", "subcode": "SCHEMA_MISMATCH"}
}

# A schema the bundle does not pin is not one the rules were written for.
test_schema_of_an_unpinned_service_denies if {
	output := fx.graphql("bri", fx.income_fields("consent:identity", fx.literal([2025])))
	result := gbo.response with input as fx.request_with(fx.hv, "onbekend", output, fx.scope_headers("bd:ib:2025"))
		with data.dvtp.gbo.consent.resolved as _consent
	result.context.reason_admin.subcode == "SCHEMA_MISMATCH"
}

# What the mapper could not verify, the engine passes on unchanged. The
# Inway hands the body to the PDP only for Content-Type exactly
# application/json; with anything else the mapper sees no body.
test_unverifiable_request_denies_with_the_mappers_code if {
	output := {
		"profile": "ftv-graphql/0.1",
		"operation": null,
		"schema": {"digest": data.dvtp.gbo.graphql_schemas.digests.bri},
		"fields": [],
		"unverifiable": {"code": "COVERAGE_UNVERIFIABLE", "subcode": "UNSUPPORTED_TRANSPORT", "message": "body absent or not a string"},
	}
	result := gbo.response with input as fx.request_with(fx.hv, "bri", output, fx.scope_headers("bd:ib:2025"))
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	result.context.reason_admin == {"code": "COVERAGE_UNVERIFIABLE", "subcode": "UNSUPPORTED_TRANSPORT"}
}

# Without a schema there is no pin to compare: the mapper's code stands.
test_unavailable_schema_denies_with_the_mappers_code if {
	output := {
		"profile": "ftv-graphql/0.1",
		"operation": null,
		"schema": null,
		"fields": [],
		"unverifiable": {"code": "CONFIG_ERROR", "subcode": "SCHEMA_UNAVAILABLE", "message": "no GraphQL schema loaded"},
	}
	result := gbo.response with input as fx.request_with(fx.hv, "bri", output, {})
	result.context.reason_admin == {"code": "CONFIG_ERROR", "subcode": "SCHEMA_UNAVAILABLE"}
}

test_typename_alone_has_no_data_fields if {
	typename := {"path": ["__typename"], "parentType": "Query", "field": "__typename", "leaf": true}
	result := gbo.response with input as fx.request(fx.hv, "bri", [typename], fx.scope_headers("bd:ib:2025"))
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	result.context.reason_admin == {"code": "NO_DATA_FIELDS"}
}

# ── Root fields are data fields ──────────────────────────────────────────
# A root field runs a resolver with its arguments. It is bound by its own
# key, and each selection is judged on its own arguments.

# Two persons under two aliases: the second names a BSN the caller chose.
test_each_selection_of_the_root_is_judged_on_its_own_argument if {
	fields := array.concat(
		fx.income_fields("consent:identity", fx.literal([2025])),
		[
			fx.person_as(["b"], fx.literal("999991772")),
			{"path": ["b", "__typename"], "parentType": "IngeschrevenPersoon", "field": "__typename", "leaf": true},
		],
	)
	result := gbo.response with input as fx.request(fx.hv, "bri", fields, fx.scope_headers("bd:ib:2025"))
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	result.context.reason_admin.code == "CONSTRAINT_MISMATCH"
	[d.field | some d in result.context.denied_fields] == ["b"]
}

# Introspection is a root field like any other, and no rule covers it.
test_introspection_next_to_a_permitted_query_denies if {
	fields := array.concat(
		fx.income_fields("consent:identity", fx.literal([2025])),
		[
			{"path": ["__schema"], "parentType": "Query", "field": "__schema", "leaf": false},
			{"path": ["__schema", "types"], "parentType": "__Schema", "field": "types", "leaf": false},
			{"path": ["__schema", "types", "name"], "parentType": "__Type", "field": "name", "leaf": true},
		],
	)
	result := gbo.response with input as fx.request(fx.hv, "bri", fields, fx.scope_headers("bd:ib:2025"))
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	[d.key | some d in result.context.denied_fields] == ["Query.__schema"]
	result.context.denied_fields[0].code == "NO_APPLICABLE_RULE"
}

# ── Arguments are read from the field that carries them ─────────────────

test_years_from_a_schema_default_deny if {
	fields := [
		fx.person("consent:identity"),
		fx.declarations(fx.schema_default([2025])),
		fx.year,
	]
	result := gbo.response with input as fx.request(fx.hv, "bri", fields, fx.scope_headers("bd:ib:2025"))
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	result.context.reason_admin.code == "YEAR_NOT_COVERED"
}

test_years_written_as_a_variable_are_read if {
	fields := [fx.person("consent:identity"), fx.declarations(fx.variable("jaren", [2025])), fx.year]
	result := gbo.response with input as fx.request(fx.hv, "bri", fields, fx.scope_headers("bd:ib:2025"))
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == true
}

# An edge DVT0001 does not cover is denied, also when its leaves would
# inherit coverage from their type.
test_edge_outside_the_rule_denies if {
	fields := array.concat(
		fx.income_fields("consent:identity", fx.literal([2025])),
		[fx.box("box2Inkomen"), fx.amount("box2Inkomen")],
	)
	result := gbo.response with input as fx.request(fx.hv, "bri", fields, fx.scope_headers("bd:ib:2025"))
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	[d.key | some d in result.context.denied_fields] == ["AangifteIH.box2Inkomen"]
	result.context.reason_admin.code == "NO_APPLICABLE_RULE"
}

# ── The regime decides which rules are evaluated ─────────────────────────
# Under a consent token only consent rules judge a field, without one only
# PID rules. The trace of a denied field shows exactly those.

_evaluated_rules(result) := {e.rule | some d in result.context.denied_fields; some e in d.evaluated}

test_a_consent_request_is_judged_by_consent_rules_only if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "consent:identity", [2024])
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	_evaluated_rules(result) == {"DVT0001"}
}

test_a_pid_request_is_judged_by_pid_rules_only if {
	result := gbo.response with input as _eudi_input("99999999900000000100", 2023)
	result.decision == false
	_evaluated_rules(result) == {"EUD0001"}
}

# ── Deny-reason surfacing for consent-based requests ─────────────────────
# Fields covered by both DVT0001 and EUD0001 (the root, the declarations,
# box1Inkomen). Under a consent token only DVT0001 judges them, so the
# reason is its own, never EUD0001's PID_NOT_PRESENT.

_consent := {
	"context_valid": true,
	"status_available": true,
	"exists": true,
	"withdrawn": false,
	"valid_until": "2030-01-01T00:00:00Z",
	"granted_scopes": ["bd:ib:2025"],
	"dienstverlener_oin": "99999999900000000300",
}

# The consent itself is valid throughout — each case fails on exactly one
# DvTP axis, so the asserted code is the genuine reason and nothing else.
# The consent is supplied per test, as the resolved one.
_dvtp_input(scope, bsn, years) := fx.income(fx.hv, bsn, years, fx.scope_headers(scope))

test_dvtp_deny_surfaces_year_not_covered if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "consent:identity", [2024])
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	result.context.reason_admin.code == "YEAR_NOT_COVERED"
}

test_dvtp_deny_surfaces_consent_scope_mismatch if {
	result := gbo.response with input as _dvtp_input("bd:ib:2023", "consent:identity", [2025])
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	result.context.reason_admin.code == "CONSENT_SCOPE_MISMATCH"
}

test_dvtp_deny_surfaces_constraint_mismatch if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "999991772", [2025])
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	result.context.reason_admin.code == "CONSTRAINT_MISMATCH"
}

# A placeholder the rule does not accept is denied as a whole request, not
# passed on for the source to refuse.
test_dvtp_deny_placeholder_the_rule_does_not_accept if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "consent:pseudonym", [2025])
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	result.context.reason_admin.code == "CONSTRAINT_MISMATCH"
}

# ── Deny-reason surfacing for PID-based requests ─────────────────────────
# The mirror of the cases above: without a consent token only the EUDI
# rules judge, so DVT0001's CONSENT_CONTEXT_INVALID never hides the
# genuine EUDI reason.

_eudi_input(actor, year) := fx.income(actor, "999991772", [year], {})

test_eudi_deny_surfaces_year_not_allowed if {
	result := gbo.response with input as _eudi_input("99999999900000000100", 2023)
	result.decision == false
	result.context.reason_admin.code == "YEAR_NOT_ALLOWED"
}

test_eudi_deny_surfaces_actor_not_allowed if {
	result := gbo.response with input as _eudi_input("99999999900000000999", 2024)
	result.decision == false
	result.context.reason_admin.code == "ACTOR_NOT_ALLOWED"
}

# ── Evidence decides the regime ──────────────────────────────────────────
# Every rule covering the field is evaluated on every request. A consent
# token selects the consent regime; its absence, the PID regime. There is
# no third case: "both" cannot occur when one regime is defined as the
# absence of the other.

test_dvtp_allow_grants_via_consent_rule if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "consent:identity", [2025])
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == true
	every g in result.context.granted {
		g.rule == "DVT0001"
	}
}

# The decision log records the consent the decision was taken on: it is not
# in input, so the response document carries it.
test_dvtp_response_carries_the_consent if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "consent:identity", [2025])
		with data.dvtp.gbo.consent.resolved as _consent
	result.context.pip.consent == _consent
}

# No consent token, so the request is judged under the PID regime. A
# placeholder stands for the subject of a consent, and there is none: the
# query names nobody, whichever placeholder it uses.
test_no_consent_token_and_a_placeholder_names_no_subject if {
	every placeholder in ["consent:identity", "consent:pseudonym"] {
		result := gbo.response with input as _dvtp_input("bd:ib:2025", placeholder, [2025])
		result.decision == false
		result.context.reason_admin.code == "PID_NOT_PRESENT"
	}
}

# A consent-based consumer that omits its token and names a real BSN meets
# the EUDI rules' allowed_actors, not its consent — which is why that
# whitelist must stay disjoint from the consent-based consumers (test below).
test_no_consent_token_and_a_real_bsn_meets_the_actor_check if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "999991772", [2025])
	result.decision == false
	result.context.reason_admin.code == "ACTOR_NOT_ALLOWED"
}

# ── The invariant the PID regime now rests on ────────────────────────────
# A consent token selects the consent regime and its absence the PID
# regime. Absence of a header is therefore what puts a request under the
# EUDI rules, and the only gate left on them is each rule's own
# allowed_actors.
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
# DvTP contract, or the PID regime needs positive evidence of its own (a
# verified PID assertion would remove the fall-through).
_dvtp_consumer_oins := {
	"99999999900000000300", # HV, seed-bri-connection-hv.sh
	"99999999900000001100", # integrator for HV, seed-bri-delegation-int.sh
}

test_pid_rule_actors_are_disjoint_from_consent_consumers if {
	every rule_id in {"EUD0001", "EUD0002"} {
		actors := object.get(gbo._rule_meta[rule_id].spec, "allowed_actors", set())
		count(actors) > 0
		count(actors & _dvtp_consumer_oins) == 0
	}
}

# ── Every GBO rule names one regime ──────────────────────────────────────
# A rule that names none is evaluated in both regimes; one that names both
# would be judged as a consent rule only. Neither is meant for a GBO rule.

_regime_violations contains sprintf("%s: names no regime", [rid]) if {
	some rid, m in gbo._rule_meta
	not _names_regime(m.spec, "consent_required")
	not _names_regime(m.spec, "pid_required")
}

_regime_violations contains sprintf("%s: names both regimes", [rid]) if {
	some rid, m in gbo._rule_meta
	_names_regime(m.spec, "consent_required")
	_names_regime(m.spec, "pid_required")
}

_names_regime(spec, flag) if object.get(spec, flag, false) == true

test_every_rule_names_one_regime if {
	violations := _regime_violations
	print("rules without exactly one regime:", violations)
	count(violations) == 0
}

test_regime_check_catches_none_and_both if {
	broken := {
		"none": {"rule_id": "N", "covers_fields": set(), "spec": {"rule_id": "N"}},
		"both": {"rule_id": "B", "covers_fields": set(), "spec": {"rule_id": "B", "consent_required": true, "pid_required": true}},
	}
	violations := _regime_violations with data.dvtp.gbo.rules as broken
	violations == {"N: names no regime", "B: names both regimes"}
}

# ── Every check that reads an argument names it ──────────────────────────
# A rule that turns on a year, PID or placeholder check without naming the
# field and argument it reads denies every request at runtime (fail closed).
# This catches the omission before the rule reaches a PDP.

_argument_violations contains sprintf("%s: %s without years_argument", [rid, check]) if {
	some rid, m in gbo._rule_meta
	some check in {"allowed_years", "years_in_scopes"}
	not object.get(m.spec, check, false) in {false, set(), []}
	not _names_argument(object.get(m.spec, "years_argument", null))
}

_argument_violations contains sprintf("%s: pid_required without subject_argument", [rid]) if {
	some rid, m in gbo._rule_meta
	m.spec.pid_required == true
	not _names_argument(object.get(m.spec, "subject_argument", null))
}

_argument_violations contains sprintf("%s: constraint_binding entry without field and arg", [rid]) if {
	some rid, m in gbo._rule_meta
	some binding in object.get(m.spec, "constraint_binding", [])
	not _names_argument(binding)
}

_names_argument(binding) if {
	is_string(binding.field)
	contains(binding.field, ".")
	is_string(binding.arg)
	binding.arg != ""
}

test_every_argument_check_names_its_argument if {
	violations := _argument_violations
	print("rules with a check that names no argument:", violations)
	count(violations) == 0
}

# The check itself catches each omission.
test_argument_check_catches_a_rule_that_names_none if {
	broken := {
		"years": {"rule_id": "Y", "covers_fields": set(), "spec": {"rule_id": "Y", "allowed_years": {2025}}},
		"pid": {"rule_id": "P", "covers_fields": set(), "spec": {"rule_id": "P", "pid_required": true}},
		"bsn": {"rule_id": "B", "covers_fields": set(), "spec": {"rule_id": "B", "constraint_binding": [{"arg": "bsn", "placeholders": {"consent:identity"}}]}},
	}
	violations := _argument_violations with data.dvtp.gbo.rules as broken
	violations == {
		"Y: allowed_years without years_argument",
		"P: pid_required without subject_argument",
		"B: constraint_binding entry without field and arg",
	}
}

# ── Failed evidence gives the regime's own reason ────────────────────────
# A PID-regime request without a subject surfaces PID_NOT_PRESENT; a
# consent token that does not verify surfaces the consent's reason.

test_pid_regime_without_a_subject_surfaces_pid_not_present if {
	result := gbo.response with input as fx.income(fx.eudi_issuer, "", [2024], {})
	result.decision == false
	result.context.reason_admin.code == "PID_NOT_PRESENT"
}

# Without a subject, the other fields fail on the next check — here the
# actor, as the mortgage provider is no designated issuer. The missing
# subject is still the request's reason.
test_pid_regime_without_a_subject_outranks_the_actor if {
	result := gbo.response with input as fx.income(fx.hv, "", [2025], {})
	result.decision == false
	"ACTOR_NOT_ALLOWED" in {d.code | some d in result.context.denied_fields}
	result.context.reason_admin.code == "PID_NOT_PRESENT"
}

# The exception is the PID regime's alone: under a consent token the worst
# field reason stands, here the integrator's over a field no consent rule
# covers.
test_consent_regime_keeps_the_worst_field_reason if {
	fields := array.concat(
		fx.income_fields("consent:identity", fx.literal([2025])),
		[fx.box("box2Inkomen")],
	)
	result := gbo.response with input as object.union(
		fx.request(_integrator, "bri", fields, fx.scope_headers("bd:ib:2025")),
		{"subject": {"attributes": {"outway_delegator_peer_id": "99999999900000000300"}}},
	)
		with data.dvtp.gbo.consent.resolved as _consent
		with data.entities.dvtp_participant as {}
	result.decision == false
	"NO_APPLICABLE_RULE" in {d.code | some d in result.context.denied_fields}
	result.context.reason_admin.code == "INTEGRATOR_NOT_REGISTERED"
}

_unverifiable_input := fx.income(fx.hv, "", [2025], {})

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

# ── Delegated calls through the engine ───────────────────────────────────
# The mandate comes from the admission register (data.entities, pulled by
# OpenFTV), never from input. These cases cover the wiring the rule tests
# stand in for with a ready-made pip.integrator.

_integrator := "99999999900000001100"

_registered := {_integrator: {
	"name": "Demo Integrator BV",
	"active": true,
	"allowed_source_peer_ids": ["99999999900000000200"],
	"acts_for": [{"peer_id": "99999999900000000300", "rules": ["DVT0001"]}],
}}

_delegated_input(integrator) := object.union(
	_dvtp_input("bd:ib:2025", "consent:identity", [2025]),
	{"subject": {
		"id": integrator,
		"attributes": {"outway_delegator_peer_id": "99999999900000000300"},
	}},
)

test_delegated_call_by_registered_integrator_allowed if {
	result := gbo.response with input as _delegated_input(_integrator)
		with data.dvtp.gbo.consent.resolved as _consent
		with data.entities.dvtp_participant as _registered
	result.decision == true
	result.context.granted[0].rule == "DVT0001"
}

test_delegated_call_by_unregistered_integrator_denied if {
	result := gbo.response with input as _delegated_input("99999999900000001199")
		with data.dvtp.gbo.consent.resolved as _consent
		with data.entities.dvtp_participant as _registered
	result.decision == false
	result.context.reason_admin.code == "INTEGRATOR_NOT_REGISTERED"
}

test_delegated_call_without_admission_feed_denied if {
	result := gbo.response with input as _delegated_input(_integrator)
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	result.context.reason_admin.code == "INTEGRATOR_NOT_REGISTERED"
}

# A mandate in input is not one the policy pulled: dropped, like pip.consent.
test_input_pip_integrator_is_not_trusted if {
	forged := {"integrator": _registered[_integrator]}
	req := object.union(_delegated_input(_integrator), {"context": {"pip": forged}})
	ctx := gbo._ctx with input as req
		with data.dvtp.gbo.consent.resolved as _consent
	not ctx.pip.integrator
	result := gbo.response with input as req
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	result.context.reason_admin.code == "INTEGRATOR_NOT_REGISTERED"
}

# A direct call carries no mandate into the rules' context at all.
test_direct_call_carries_no_integrator_pip if {
	ctx := gbo._ctx with input as _dvtp_input("bd:ib:2025", "consent:identity", [2025])
		with data.dvtp.gbo.consent.resolved as _consent
		with data.entities.dvtp_participant as _registered
	not ctx.pip.integrator
}

# Leaving the consent token out does not get a registered integrator into
# the PID regime: the EUDI rules check the peer that connects.
test_delegated_call_without_consent_token_denied if {
	req := object.union(fx.income(_integrator, "999991772", [2024], {}), {"subject": {"attributes": {"outway_delegator_peer_id": "99999999900000000300"}}})
	result := gbo.response with input as req
		with data.entities.dvtp_participant as _registered
	result.decision == false
}
