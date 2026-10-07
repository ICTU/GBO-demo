package dvtp.gbo_test

import data.dvtp.gbo
import data.dvtp.gbo.fixtures_test as fx

# The policy resolves the consent itself (tested in consent_test.rego). These
# tests stand in a resolved consent with `with`, so they test the engine alone.

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

test_ctx_resource_placeholders_empty_without_consent if {
	ctx := gbo._ctx with input as {"subject": {"type": "org", "id": "x"}, "context": {}}
	ctx.resource.subject_placeholders == set()
}

test_ctx_resource_placeholders_empty_for_an_unverified_consent if {
	ctx := gbo._ctx with input as _input with data.dvtp.gbo.consent.resolved as {"context_valid": false, "invalid_code": "CONSENT_SIGNATURE_INVALID"}
	ctx.resource.subject_placeholders == set()
}

# The policy decides which placeholders exist; a request cannot add its own.
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

# ── What the administrator sees (profile Section 10.1) ───────────────────

test_admin_view_of_a_refused_field if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "consent:identity", [2024])
		with data.dvtp.gbo.consent.resolved as _consent
	admin := fx.admin(result)
	admin.code == "FIELD_NOT_PERMITTED"
	admin.schema_digest == data.dvtp.gbo.graphql_schemas.digests.bri
	count(admin.denied_fields) == 1
	some denied in admin.denied_fields
	denied.key == "IngeschrevenPersoon.heeftBelastingjaarAangifte"
	denied.index == 1
	denied.path == ["ingeschrevenPersoon", "heeftBelastingjaarAangifte"]
	denied.code == "YEAR_NOT_COVERED"
	denied.evaluated == ["DVT0001"]
	denied.trace[0].rule == "DVT0001"
	some step in denied.trace[0].steps
	step.code == "YEAR_NOT_COVERED"
	step.status == "fail"
}

# The consumer's view and the texts hide which field was refused; only the
# administrator's view names it.
test_consumer_view_names_no_field if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "consent:identity", [2024])
		with data.dvtp.gbo.consent.resolved as _consent
	fx.client(result) == {"code": "FIELD_NOT_PERMITTED"}
	result.context.reason_user == {"en": "Field not permitted"}
	result.context.reason_admin == result.context.reason_user
}

# A request the mapper could not verify has no fields to list; the
# administrator gets the mapper's message, the consumer only the subcode.
test_admin_view_of_an_unverifiable_request if {
	output := {
		"profile": "ftv-graphql/0.1",
		"operation": null,
		"schema": {"digest": data.dvtp.gbo.graphql_schemas.digests.bri},
		"fields": [],
		"unverifiable": {"code": "COVERAGE_UNVERIFIABLE", "subcode": "INVALID_QUERY", "message": "Field Selections at 1:31"},
	}
	result := gbo.response with input as fx.request_with(fx.hv, "bri", output, {})
	fx.admin(result) == {"code": "COVERAGE_UNVERIFIABLE", "subcode": "INVALID_QUERY", "message": "Field Selections at 1:31", "schema_digest": data.dvtp.gbo.graphql_schemas.digests.bri}
	fx.client(result) == {"code": "FIELD_NOT_PERMITTED"}
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

# A consent in input is not one the policy verified, so it is dropped. With
# no token the request is in the PID regime, where a placeholder names nobody.
test_input_pip_consent_is_not_trusted if {
	req := object.union(_dvtp_input("bd:ib:2025", "consent:identity", [2025]), {"context": {"pip": {"consent": _consent}}})
	ctx := gbo._ctx with input as req
	not ctx.pip.consent
	result := gbo.response with input as req
	result.decision == false
	fx.refused_for(result, "PID_NOT_PRESENT")
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

test_response_without_consent_carries_none if {
	result := gbo.response with input as fx.income(fx.eudi_issuer, "999991772", [2024], {})
	not result.context.pip
}

# ── The mapper's output, checked before any rule (Section 9.5) ───────────
# Each failure denies the whole request, whatever the rules would say.

test_mapper_output_missing_denies if {
	req := object.remove(_dvtp_input("bd:ib:2025", "consent:identity", [2025]), ["resource"])
	result := gbo.response with input as req with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	fx.request_code(result) == {"code": "CONFIG_ERROR", "subcode": "MAPPER_OUTPUT_MISSING"}
}

test_mapper_output_of_another_profile_denies if {
	output := object.union(fx.graphql("bri", fx.income_fields("consent:identity", fx.literal([2025]))), {"profile": "ftv-graphql/0.2"})
	result := gbo.response with input as fx.request_with(fx.hv, "bri", output, fx.scope_headers("bd:ib:2025"))
		with data.dvtp.gbo.consent.resolved as _consent
	fx.request_code(result).subcode == "MAPPER_OUTPUT_MISSING"
}

test_schema_other_than_the_pin_denies if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "consent:identity", [2025])
		with data.dvtp.gbo.consent.resolved as _consent
		with data.dvtp.gbo.graphql_schemas.digests as {"bri": "sha256:other"}
	result.decision == false
	fx.request_code(result) == {"code": "CONFIG_ERROR", "subcode": "SCHEMA_MISMATCH"}
}

# A schema the bundle does not pin is not one the rules were written for.
test_schema_of_an_unpinned_service_denies if {
	output := fx.graphql("bri", fx.income_fields("consent:identity", fx.literal([2025])))
	result := gbo.response with input as fx.request_with(fx.hv, "onbekend", output, fx.scope_headers("bd:ib:2025"))
		with data.dvtp.gbo.consent.resolved as _consent
	fx.request_code(result).subcode == "SCHEMA_MISMATCH"
}

# The mapper's code passes unchanged. The Inway hands the PDP the body only
# for Content-Type exactly application/json; otherwise the mapper sees none.
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
	fx.request_code(result) == {"code": "COVERAGE_UNVERIFIABLE", "subcode": "UNSUPPORTED_TRANSPORT"}
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
	fx.request_code(result) == {"code": "CONFIG_ERROR", "subcode": "SCHEMA_UNAVAILABLE"}
}

test_typename_alone_has_no_data_fields if {
	typename := {"path": ["__typename"], "parentType": "Query", "field": "__typename", "leaf": true}
	result := gbo.response with input as fx.request(fx.hv, "bri", [typename], fx.scope_headers("bd:ib:2025"))
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	fx.request_code(result) == {"code": "NO_DATA_FIELDS"}
}

# ── Root fields are data fields ──────────────────────────────────────────
# Bound by their own key; each selection is judged on its own arguments.

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
	fx.refused_for(result, "CONSTRAINT_MISMATCH")
	[concat(".", d.path) | some d in fx.denied(result)] == ["b"]
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
	[d.key | some d in fx.denied(result)] == ["Query.__schema"]
	fx.denied(result)[0].code == "NO_APPLICABLE_RULE"
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
	fx.refused_for(result, "YEAR_NOT_COVERED")
}

test_years_written_as_a_variable_are_read if {
	fields := [fx.person("consent:identity"), fx.declarations(fx.variable("jaren", [2025])), fx.year]
	result := gbo.response with input as fx.request(fx.hv, "bri", fields, fx.scope_headers("bd:ib:2025"))
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == true
}

# Denied even though the edge's leaves would inherit coverage from their type.
test_edge_outside_the_rule_denies if {
	fields := array.concat(
		fx.income_fields("consent:identity", fx.literal([2025])),
		[fx.box("box2Inkomen"), fx.amount("box2Inkomen")],
	)
	result := gbo.response with input as fx.request(fx.hv, "bri", fields, fx.scope_headers("bd:ib:2025"))
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	[d.key | some d in fx.denied(result)] == ["AangifteIH.box2Inkomen"]
	fx.refused_for(result, "NO_APPLICABLE_RULE")
}

# ── The regime decides which rules are evaluated ─────────────────────────
# The trace of a denied field shows only the rules of the request's regime.

_evaluated_rules(result) := {rule | some d in fx.denied(result); some rule in d.evaluated}

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

# ── Deny reasons under a consent token ───────────────────────────────────
# DVT0001 and EUD0001 cover the same fields; only DVT0001 judges them here,
# so the reason is its own, never EUD0001's PID_NOT_PRESENT.

_consent := {
	"context_valid": true,
	"status_available": true,
	"exists": true,
	"withdrawn": false,
	"valid_until": "2030-01-01T00:00:00Z",
	"granted_scopes": ["bd:ib:2025"],
	"dienstverlener_oin": "99999999900000000300",
}

# The consent is valid throughout and supplied per test; each case fails on
# exactly one check, so the asserted code is the genuine reason.
_dvtp_input(scope, bsn, years) := fx.income(fx.hv, bsn, years, fx.scope_headers(scope))

test_dvtp_deny_surfaces_year_not_covered if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "consent:identity", [2024])
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	fx.refused_for(result, "YEAR_NOT_COVERED")
}

test_dvtp_deny_surfaces_consent_scope_mismatch if {
	result := gbo.response with input as _dvtp_input("bd:ib:2023", "consent:identity", [2025])
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	fx.refused_for(result, "CONSENT_SCOPE_MISMATCH")
}

test_dvtp_deny_surfaces_constraint_mismatch if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "999991772", [2025])
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	fx.refused_for(result, "CONSTRAINT_MISMATCH")
}

# Denied here rather than passed on for the source to refuse.
test_dvtp_deny_placeholder_the_rule_does_not_accept if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "consent:pseudonym", [2025])
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	fx.refused_for(result, "CONSTRAINT_MISMATCH")
}

# ── Deny reasons without a consent token ─────────────────────────────────
# Only the EUDI rules judge, so DVT0001's CONSENT_CONTEXT_INVALID never hides
# the EUDI reason.

_eudi_input(actor, year) := fx.income(actor, "999991772", [year], {})

test_eudi_deny_surfaces_year_not_allowed if {
	result := gbo.response with input as _eudi_input("99999999900000000100", 2023)
	result.decision == false
	fx.refused_for(result, "YEAR_NOT_ALLOWED")
}

test_eudi_deny_surfaces_actor_not_allowed if {
	result := gbo.response with input as _eudi_input("99999999900000000999", 2024)
	result.decision == false
	fx.refused_for(result, "ACTOR_NOT_ALLOWED")
}

# ── Evidence decides the regime ──────────────────────────────────────────
# A token selects the consent regime, its absence the PID regime: no third.

test_dvtp_allow_grants_via_consent_rule if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "consent:identity", [2025])
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == true
	every g in result.context.granted {
		g.rule == "DVT0001"
	}
}

# The consent is not in input, so the response carries it for the log.
test_dvtp_response_carries_the_consent if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "consent:identity", [2025])
		with data.dvtp.gbo.consent.resolved as _consent
	result.context.pip.consent == _consent
}

# Without a consent token a placeholder stands for nobody.
test_no_consent_token_and_a_placeholder_names_no_subject if {
	every placeholder in ["consent:identity", "consent:pseudonym"] {
		result := gbo.response with input as _dvtp_input("bd:ib:2025", placeholder, [2025])
		result.decision == false
		fx.refused_for(result, "PID_NOT_PRESENT")
	}
}

# A consent-based consumer that omits its token and names a real BSN meets
# only the EUDI rules' allowed_actors: hence the invariant below.
test_no_consent_token_and_a_real_bsn_meets_the_actor_check if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "999991772", [2025])
	result.decision == false
	fx.refused_for(result, "ACTOR_NOT_ALLOWED")
}

# ── Invariant: EUDI actors are never consent-based consumers ─────────────
# Without a consent token a request falls under the EUDI rules, gated only by
# each rule's allowed_actors. An OIN that is both a designated issuer and a
# consent-based consumer could skip the citizen's consent, with its scope and
# year checks, by omitting the token. This is a deployment fact, so the test
# pins the seeded consumers.
#
# If it fails, do NOT widen this test. Either that OIN must not hold a DvTP
# contract, or the PID regime needs evidence of its own (a verified PID).
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
# A rule naming none is evaluated in both regimes; one naming both is judged
# as a consent rule only.

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
# Such a check without its field and argument denies every request (fail
# closed); this catches the omission before the rule reaches a PDP.

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

test_pid_regime_without_a_subject_surfaces_pid_not_present if {
	result := gbo.response with input as fx.income(fx.eudi_issuer, "", [2024], {})
	result.decision == false
	fx.refused_for(result, "PID_NOT_PRESENT")
}

# Each field keeps its own reason: the person field the missing subject,
# the fields below it the actor (the mortgage provider is no designated
# issuer). The consumer is told only that fields were refused.
test_pid_regime_without_a_subject_keeps_each_fields_reason if {
	result := gbo.response with input as fx.income(fx.hv, "", [2025], {})
	[d.code | some d in fx.denied(result); d.key == "Query.ingeschrevenPersoon"] == ["PID_NOT_PRESENT"]
	"ACTOR_NOT_ALLOWED" in fx.field_codes(result)
	fx.client(result) == {"code": "FIELD_NOT_PERMITTED"}
}

# Under a consent token too, each field keeps its own reason.
test_consent_regime_fields_keep_their_own_reasons if {
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
	"NO_APPLICABLE_RULE" in {d.code | some d in fx.denied(result)}
	fx.refused_for(result, "INTEGRATOR_NOT_REGISTERED")
}

_unverifiable_input := fx.income(fx.hv, "", [2025], {})

test_unverifiable_consent_surfaces_consent_context_invalid if {
	result := gbo.response with input as _unverifiable_input
		with data.dvtp.gbo.consent.resolved as {"context_valid": false, "status_available": false, "exists": false}
	result.decision == false
	fx.refused_for(result, "CONSENT_CONTEXT_INVALID")
}

# The engine reports the specific code the consent PIP gives.
test_unverifiable_consent_surfaces_its_invalid_code if {
	result := gbo.response with input as _unverifiable_input
		with data.dvtp.gbo.consent.resolved as {
			"context_valid": false,
			"status_available": false,
			"exists": false,
			"invalid_code": "CONSENT_SIGNATURE_INVALID",
		}
	result.decision == false
	fx.refused_for(result, "CONSENT_SIGNATURE_INVALID")
}

# ── Delegated calls through the engine ───────────────────────────────────
# The mandate comes from the admission register (data.entities), never from
# input. The rule tests use a ready-made pip.integrator; these test the wiring.

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
	fx.refused_for(result, "INTEGRATOR_NOT_REGISTERED")
}

# No admission register at all is a PIP failure, not a missing mandate: the
# consumer learns only that access was denied.
test_delegated_call_without_admission_register_is_pip_unavailable if {
	result := gbo.response with input as _delegated_input(_integrator)
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == false
	fx.client(result) == {"code": "ACCESS_DENIED"}
	fx.request_code(result) == {"code": "PIP_UNAVAILABLE"}
	fx.field_codes(result) == {"PIP_UNAVAILABLE"}
	some d in fx.denied(result)
	some t in d.trace
	some step in t.steps
	step.code == "INTEGRATOR_REGISTER_UNAVAILABLE"
	step.status == "fail"
}

# An empty register is a register: nobody is admitted.
test_delegated_call_with_empty_admission_register_denied if {
	result := gbo.response with input as _delegated_input(_integrator)
		with data.dvtp.gbo.consent.resolved as _consent
		with data.entities.dvtp_participant as {}
	fx.refused_for(result, "INTEGRATOR_NOT_REGISTERED")
}

# A direct call has no integrator, so the register is not needed.
test_direct_call_without_admission_register_allowed if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "consent:identity", [2025])
		with data.dvtp.gbo.consent.resolved as _consent
	result.decision == true
}

# A mandate in input is not one the policy pulled: dropped, like pip.consent.
test_input_pip_integrator_is_not_trusted if {
	forged := {"integrator": _registered[_integrator]}
	req := object.union(_delegated_input(_integrator), {"context": {"pip": forged}})
	ctx := gbo._ctx with input as req
		with data.dvtp.gbo.consent.resolved as _consent
		with data.entities.dvtp_participant as {}
	not ctx.pip.integrator
	result := gbo.response with input as req
		with data.dvtp.gbo.consent.resolved as _consent
		with data.entities.dvtp_participant as {}
	result.decision == false
	fx.refused_for(result, "INTEGRATOR_NOT_REGISTERED")
}

# Nor is the register's availability: only the engine decides it is missing.
test_input_pip_register_state_is_not_trusted if {
	forged := {"integrator_register": "unavailable"}
	req := object.union(_delegated_input(_integrator), {"context": {"pip": forged}})
	result := gbo.response with input as req
		with data.dvtp.gbo.consent.resolved as _consent
		with data.entities.dvtp_participant as _registered
	result.decision == true
}

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
