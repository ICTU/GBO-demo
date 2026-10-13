package gbo_test

import data.gbo
import data.gbo.fixtures_test as fx

# The policy resolves the consent itself (tested in consent_test.rego). These
# tests stand in a resolved consent with `with`, so they test the engine alone.

# ── What the administrator sees (profile Section 10.1) ───────────────────

test_admin_view_of_a_refused_field if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "consent:identity", [2024])
		with data.gbo.consent.resolved as _consent
	admin := fx.admin(result)
	admin.code == "FIELD_NOT_PERMITTED"
	admin.schema_digest == data.gbo.graphql_schemas.digests.bri
	count(admin.denied_fields) == 1
	some denied in admin.denied_fields
	denied.key == "IngeschrevenPersoon.heeftBelastingjaarAangifte"
	denied.index == 1
	denied.path == ["ingeschrevenPersoon", "heeftBelastingjaarAangifte"]
	denied.code == "YEAR_NOT_COVERED"
	denied.evaluated == ["DVT0001"]
	denied.trace == [{"rule": "DVT0001", "code": "YEAR_NOT_COVERED", "reasons": ["YEAR_NOT_COVERED"]}]
}

# The consumer's view and the texts hide which field was refused; only the
# administrator's view names it.
test_consumer_view_names_no_field if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "consent:identity", [2024])
		with data.gbo.consent.resolved as _consent
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
		"schema": {"digest": data.gbo.graphql_schemas.digests.bri},
		"fields": [],
		"unverifiable": {"code": "COVERAGE_UNVERIFIABLE", "subcode": "INVALID_QUERY", "message": "Field Selections at 1:31"},
	}
	result := gbo.response with input as fx.request_with(fx.hv, "bri", output, {})
	fx.admin(result) == {"code": "COVERAGE_UNVERIFIABLE", "subcode": "INVALID_QUERY", "message": "Field Selections at 1:31", "schema_digest": data.gbo.graphql_schemas.digests.bri}
	fx.client(result) == {"code": "FIELD_NOT_PERMITTED"}
}

# A consent in input is not one the policy verified, so it is dropped. With
# no token the request is in the PID regime, where a placeholder names nobody.
test_input_pip_consent_is_not_trusted if {
	req := object.union(_dvtp_input("bd:ib:2025", "consent:identity", [2025]), {"context": {"pip": {"consent": _consent}}})
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
	result := gbo.response with input as req with data.gbo.consent.resolved as _consent
	result.decision == false
	fx.request_code(result) == {"code": "CONFIG_ERROR", "subcode": "MAPPER_OUTPUT_MISSING"}
}

test_mapper_output_of_another_profile_denies if {
	output := object.union(fx.graphql("bri", fx.income_fields("consent:identity", fx.literal([2025]))), {"profile": "ftv-graphql/0.2"})
	result := gbo.response with input as fx.request_with(fx.hv, "bri", output, fx.scope_headers("bd:ib:2025"))
		with data.gbo.consent.resolved as _consent
	fx.request_code(result).subcode == "MAPPER_OUTPUT_MISSING"
}

test_schema_other_than_the_pin_denies if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "consent:identity", [2025])
		with data.gbo.consent.resolved as _consent
		with data.gbo.graphql_schemas.digests as {"bri": "sha256:other"}
	result.decision == false
	fx.request_code(result) == {"code": "CONFIG_ERROR", "subcode": "SCHEMA_MISMATCH"}
}

# A schema the bundle does not pin is not one the rules were written for.
test_schema_of_an_unpinned_service_denies if {
	output := fx.graphql("bri", fx.income_fields("consent:identity", fx.literal([2025])))
	result := gbo.response with input as fx.request_with(fx.hv, "onbekend", output, fx.scope_headers("bd:ib:2025"))
		with data.gbo.consent.resolved as _consent
	fx.request_code(result).subcode == "SCHEMA_MISMATCH"
}

# The mapper's code passes unchanged. The Inway hands the PDP the body only
# for Content-Type exactly application/json; otherwise the mapper sees none.
test_unverifiable_request_denies_with_the_mappers_code if {
	output := {
		"profile": "ftv-graphql/0.1",
		"operation": null,
		"schema": {"digest": data.gbo.graphql_schemas.digests.bri},
		"fields": [],
		"unverifiable": {"code": "COVERAGE_UNVERIFIABLE", "subcode": "UNSUPPORTED_TRANSPORT", "message": "body absent or not a string"},
	}
	result := gbo.response with input as fx.request_with(fx.hv, "bri", output, fx.scope_headers("bd:ib:2025"))
		with data.gbo.consent.resolved as _consent
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
		with data.gbo.consent.resolved as _consent
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
		with data.gbo.consent.resolved as _consent
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
		with data.gbo.consent.resolved as _consent
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
		with data.gbo.consent.resolved as _consent
	result.decision == false
	fx.refused_for(result, "YEAR_NOT_COVERED")
}

test_years_written_as_a_variable_are_read if {
	fields := [fx.person("consent:identity"), fx.declarations(fx.variable("jaren", [2025])), fx.year]
	result := gbo.response with input as fx.request(fx.hv, "bri", fields, fx.scope_headers("bd:ib:2025"))
		with data.gbo.consent.resolved as _consent
	result.decision == true
}

# Denied even though the edge's leaves would inherit coverage from their type.
test_edge_outside_the_rule_denies if {
	fields := array.concat(
		fx.income_fields("consent:identity", fx.literal([2025])),
		[fx.box("box2Inkomen"), fx.amount("box2Inkomen")],
	)
	result := gbo.response with input as fx.request(fx.hv, "bri", fields, fx.scope_headers("bd:ib:2025"))
		with data.gbo.consent.resolved as _consent
	result.decision == false
	[d.key | some d in fx.denied(result)] == ["AangifteIH.box2Inkomen"]
	fx.refused_for(result, "NO_APPLICABLE_RULE")
}

# ── The regime decides which rules are evaluated ─────────────────────────
# The trace of a denied field shows only the rules of the request's regime.

_evaluated_rules(result) := {rule | some d in fx.denied(result); some rule in d.evaluated}

test_a_consent_request_is_judged_by_consent_rules_only if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "consent:identity", [2024])
		with data.gbo.consent.resolved as _consent
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
		with data.gbo.consent.resolved as _consent
	result.decision == false
	fx.refused_for(result, "YEAR_NOT_COVERED")
}

test_dvtp_deny_surfaces_consent_scope_mismatch if {
	result := gbo.response with input as _dvtp_input("bd:ib:2023", "consent:identity", [2025])
		with data.gbo.consent.resolved as _consent
	result.decision == false
	fx.refused_for(result, "CONSENT_SCOPE_MISMATCH")
}

test_dvtp_deny_surfaces_constraint_mismatch if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "999991772", [2025])
		with data.gbo.consent.resolved as _consent
	result.decision == false
	fx.refused_for(result, "CONSTRAINT_MISMATCH")
}

# Denied here rather than passed on for the source to refuse.
test_dvtp_deny_placeholder_the_rule_does_not_accept if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "consent:pseudonym", [2025])
		with data.gbo.consent.resolved as _consent
	result.decision == false
	fx.refused_for(result, "CONSTRAINT_MISMATCH")
}

# The ownership consent on the person alone: refused by the scope pin.
test_dvtp_deny_an_lvg_consent_asking_only_for_the_person if {
	req := fx.request(fx.hv, "bri", [fx.person("consent:identity")], fx.scope_headers("lvg:vbo:eigendom"))
	result := gbo.response with input as req
		with data.gbo.consent.resolved as object.union(_consent, {"granted_scopes": ["lvg:vbo:eigendom"]})
	result.decision == false
	fx.field_codes(result) == {"SCOPE_NOT_ALLOWED"}
}

# Without a scope the consent covers none; that stays the code, the scope pin
# comes after it.
test_dvtp_without_a_scope_the_code_stays_consent_scope_mismatch if {
	result := gbo.response with input as fx.income(fx.hv, "consent:identity", [2025], {})
		with data.gbo.consent.resolved as _consent
	fx.field_codes(result) == {"CONSENT_SCOPE_MISMATCH"}
	some d in fx.denied(result)
	"SCOPE_NOT_ALLOWED" in d.trace[0].reasons
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
		with data.gbo.consent.resolved as _consent
	result.decision == true
	every g in result.context.granted {
		g.rule == "DVT0001"
	}
}

# The consent is not in input, so the response carries it for the log.
test_dvtp_response_carries_the_consent if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "consent:identity", [2025])
		with data.gbo.consent.resolved as _consent
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
# only the EUDI rules' designated issuers: hence the invariant below.
test_no_consent_token_and_a_real_bsn_meets_the_actor_check if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "999991772", [2025])
	result.decision == false
	fx.refused_for(result, "ACTOR_NOT_ALLOWED")
}

# ── Invariant: EUDI actors are never consent-based consumers ─────────────
# Without a consent token a request falls under the EUDI rules, gated only by
# the designated issuers. An OIN that is both a designated issuer and a
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
	issuers := data.gbo.rules.eudi.helper.designated_issuers
	count(issuers) > 0
	count(issuers & _dvtp_consumer_oins) == 0
}

# ── The service decides which rules apply ────────────────────────────────
# Query.ingeschrevenPersoon is a key of both bri and brp. A rule naming
# services binds only on those; a rule naming none on every service.

_scoped(services) := {"test": {"s": {
	"rule_id": "S",
	"covers_fields": {"Query.ingeschrevenPersoon"},
	"services": services,
	"deny": set(),
}}}

_unscoped := {"test": {"u": {
	"rule_id": "U",
	"covers_fields": {"Query.ingeschrevenPersoon"},
	"deny": set(),
}}}

_person_on(service) := fx.request(fx.hv, service, [fx.person("999991772")], {})

test_a_rule_for_brp_does_not_bind_on_bri if {
	result := gbo.response with input as _person_on("bri") with data.gbo.rules as _scoped({"brp"})
	fx.field_codes(result) == {"NO_APPLICABLE_RULE"}
}

test_a_rule_for_brp_binds_on_brp if {
	result := gbo.response with input as _person_on("brp") with data.gbo.rules as _scoped({"brp"})
	result.decision == true
	result.context.granted[0].rule == "S"
}

test_a_rule_naming_no_service_binds_on_every_service if {
	every service in ["bri", "brp"] {
		result := gbo.response with input as _person_on(service) with data.gbo.rules as _unscoped
		result.context.granted[0].rule == "U"
	}
}

# Without a signed service a scoped rule has nothing to match; the schema
# is left out, or the missing pin would deny first.
test_a_rule_for_a_service_does_not_bind_without_one if {
	req := json.remove(
		fx.request_with(fx.hv, "", object.union(fx.graphql("bri", [fx.person("999991772")]), {"schema": null}), {}),
		["subject/attributes/service_name"],
	)
	result := gbo.response with input as req with data.gbo.rules as _scoped({"brp"})
	fx.field_codes(result) == {"NO_APPLICABLE_RULE"}
	unscoped := gbo.response with input as req with data.gbo.rules as _unscoped
	unscoped.context.granted[0].rule == "U"
}

test_an_empty_service_set_binds_nowhere if {
	every service in ["bri", "brp"] {
		result := gbo.response with input as _person_on(service) with data.gbo.rules as _scoped(set())
		fx.field_codes(result) == {"NO_APPLICABLE_RULE"}
	}
}

# ── Every GBO rule names one regime ──────────────────────────────────────
# A rule naming none is evaluated in both regimes.

_regime_violations contains sprintf("%s: names no regime", [rid]) if {
	some rid, m in gbo._rule_meta
	not m.regime in {"consent", "pid"}
}

test_every_rule_names_one_regime if {
	violations := _regime_violations
	print("rules without exactly one regime:", violations)
	count(violations) == 0
}

test_regime_check_catches_a_rule_without_one if {
	broken := {"test": {
		"none": {"rule_id": "N", "covers_fields": set()},
		"other": {"rule_id": "O", "regime": "both", "covers_fields": set()},
	}}
	violations := _regime_violations with data.gbo.rules as broken
	violations == {"N: names no regime", "O: names no regime"}
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
		with data.gbo.consent.resolved as _consent
		with data.entities.dvtp_participant as {}
	result.decision == false
	"NO_APPLICABLE_RULE" in {d.code | some d in fx.denied(result)}
	fx.refused_for(result, "INTEGRATOR_NOT_REGISTERED")
}

_unverifiable_input := fx.income(fx.hv, "", [2025], {})

test_unverifiable_consent_surfaces_consent_context_invalid if {
	result := gbo.response with input as _unverifiable_input
		with data.gbo.consent.resolved as {"context_valid": false, "status_available": false, "exists": false}
	result.decision == false
	fx.refused_for(result, "CONSENT_CONTEXT_INVALID")
}

# The engine reports the specific code the consent PIP gives.
test_unverifiable_consent_surfaces_its_invalid_code if {
	result := gbo.response with input as _unverifiable_input
		with data.gbo.consent.resolved as {
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
		with data.gbo.consent.resolved as _consent
		with data.entities.dvtp_participant as _registered
	result.decision == true
	result.context.granted[0].rule == "DVT0001"
}

test_delegated_call_by_unregistered_integrator_denied if {
	result := gbo.response with input as _delegated_input("99999999900000001199")
		with data.gbo.consent.resolved as _consent
		with data.entities.dvtp_participant as _registered
	result.decision == false
	fx.refused_for(result, "INTEGRATOR_NOT_REGISTERED")
}

# No admission register at all is a PIP failure, not a missing mandate: the
# consumer learns only that access was denied.
test_delegated_call_without_admission_register_is_pip_unavailable if {
	result := gbo.response with input as _delegated_input(_integrator)
		with data.gbo.consent.resolved as _consent
	result.decision == false
	fx.client(result) == {"code": "ACCESS_DENIED"}
	fx.request_code(result) == {"code": "PIP_UNAVAILABLE"}
	fx.field_codes(result) == {"PIP_UNAVAILABLE"}
	some d in fx.denied(result)
	some t in d.trace
	"INTEGRATOR_REGISTER_UNAVAILABLE" in t.reasons
}

# An empty register is a register: nobody is admitted.
test_delegated_call_with_empty_admission_register_denied if {
	result := gbo.response with input as _delegated_input(_integrator)
		with data.gbo.consent.resolved as _consent
		with data.entities.dvtp_participant as {}
	fx.refused_for(result, "INTEGRATOR_NOT_REGISTERED")
}

# A direct call has no integrator, so the register is not needed.
test_direct_call_without_admission_register_allowed if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "consent:identity", [2025])
		with data.gbo.consent.resolved as _consent
	result.decision == true
}

# A mandate in input is not one the policy pulled: dropped, like pip.consent.
test_input_pip_integrator_is_not_trusted if {
	forged := {"integrator": _registered[_integrator]}
	req := object.union(_delegated_input(_integrator), {"context": {"pip": forged}})
	result := gbo.response with input as req
		with data.gbo.consent.resolved as _consent
		with data.entities.dvtp_participant as {}
	result.decision == false
	fx.refused_for(result, "INTEGRATOR_NOT_REGISTERED")
}

# Nor is the register's availability: only the engine decides it is missing.
test_input_pip_register_state_is_not_trusted if {
	forged := {"integrator_register": "unavailable"}
	req := object.union(_delegated_input(_integrator), {"context": {"pip": forged}})
	result := gbo.response with input as req
		with data.gbo.consent.resolved as _consent
		with data.entities.dvtp_participant as _registered
	result.decision == true
}

# Leaving the consent token out does not get a registered integrator into
# the PID regime: the EUDI rules check the peer that connects.
test_delegated_call_without_consent_token_denied if {
	req := object.union(fx.income(_integrator, "999991772", [2024], {}), {"subject": {"attributes": {"outway_delegator_peer_id": "99999999900000000300"}}})
	result := gbo.response with input as req
		with data.entities.dvtp_participant as _registered
	result.decision == false
}

# ── The mandate is the engine's, for every rule ─────────────────────────

_with_mandate(acts_for, active) := {_integrator: object.union(_registered[_integrator], {"acts_for": acts_for, "active": active})}

test_delegated_call_for_an_unregistered_provider_denied if {
	result := gbo.response with input as _delegated_input(_integrator)
		with data.gbo.consent.resolved as _consent
		with data.entities.dvtp_participant as _with_mandate([{"peer_id": "99999999900000000999", "rules": ["DVT0001"]}], true)
	fx.refused_for(result, "INTEGRATOR_NOT_REGISTERED")
}

test_delegated_call_for_an_unregistered_rule_denied if {
	result := gbo.response with input as _delegated_input(_integrator)
		with data.gbo.consent.resolved as _consent
		with data.entities.dvtp_participant as _with_mandate([{"peer_id": "99999999900000000300", "rules": ["LVG0001"]}], true)
	fx.refused_for(result, "INTEGRATOR_NOT_REGISTERED")
}

test_delegated_call_from_a_suspended_integrator_denied if {
	result := gbo.response with input as _delegated_input(_integrator)
		with data.gbo.consent.resolved as _consent
		with data.entities.dvtp_participant as _with_mandate(_registered[_integrator].acts_for, false)
	fx.refused_for(result, "INTEGRATOR_NOT_REGISTERED")
}

# The service provider cannot claim the delegator attribute for itself: a
# delegator equal to the connecting peer is a direct call, which needs no
# register.
test_delegator_equal_to_caller_is_a_direct_call if {
	req := object.union(_dvtp_input("bd:ib:2025", "consent:identity", [2025]), {"subject": {"attributes": {"outway_delegator_peer_id": fx.hv}}})
	result := gbo.response with input as req
		with data.gbo.consent.resolved as _consent
	result.decision == true
}

# The mandate comes after the rule's own reasons: an integrator that is no
# designated issuer and has no mandate is refused as no designated issuer.
# Both reasons are in the trace.
test_mandate_comes_after_the_rules_own_reasons if {
	req := object.union(fx.income(_integrator, "999991772", [2024], {}), {"subject": {"attributes": {"outway_delegator_peer_id": fx.hv}}})
	result := gbo.response with input as req
		with data.entities.dvtp_participant as _registered
	fx.field_codes(result) == {"ACTOR_NOT_ALLOWED"}
	some d in fx.denied(result)
	d.trace == [{"rule": "EUD0001", "code": "ACTOR_NOT_ALLOWED", "reasons": ["ACTOR_NOT_ALLOWED", "INTEGRATOR_NOT_REGISTERED"]}]
}

# ── A rule's code is its most fundamental reason ────────────────────────

# A token that does not verify fails most of the consent's conditions; the
# code names the token, the trace keeps the rest.
test_an_unverified_token_is_the_code_and_the_trace_keeps_the_rest if {
	result := gbo.response with input as _dvtp_input("bd:ib:2025", "consent:identity", [2025])
		with data.gbo.consent.resolved as {"context_valid": false, "status_available": false, "exists": false}
	fx.field_codes(result) == {"CONSENT_CONTEXT_INVALID"}
	some d in fx.denied(result)
	some t in d.trace
	t.code == "CONSENT_CONTEXT_INVALID"
	t.reasons[0] == "CONSENT_CONTEXT_INVALID"
	"CONSENT_WITHDRAWN" in t.reasons
	"CONSENT_ACTOR_MISMATCH" in t.reasons
}

# A reason the engine does not know still refuses, after the known ones; a
# new code can never let a field through.
_rule_with(deny) := {"test": {"r": {
	"rule_id": "R",
	"covers_fields": {"Query.ingeschrevenPersoon"},
	"deny": deny,
}}}

_person_only := fx.request(fx.hv, "bri", [fx.person("999991772")], {})

test_an_unknown_reason_refuses_after_the_known_ones if {
	result := gbo.response with input as _person_only
		with data.gbo.rules as _rule_with({"SOMETHING_NEW", "CONSENT_EXPIRED"})
	result.decision == false
	fx.denied(result)[0].trace == [{"rule": "R", "code": "CONSENT_EXPIRED", "reasons": ["CONSENT_EXPIRED", "SOMETHING_NEW"]}]
}

test_an_unknown_reason_alone_is_the_code if {
	result := gbo.response with input as _person_only
		with data.gbo.rules as _rule_with({"SOMETHING_NEW"})
	result.decision == false
	fx.field_codes(result) == {"SOMETHING_NEW"}
}

# ── A field is never dropped ─────────────────────────────────────────────
# A rule that does not evaluate (here: deny is not a set) leaves its field
# without a decision. That field is refused as a fault in the policy, not
# dropped while the other fields decide alone.
_broken_and_sound_rules := {"test": {
	"broken": {"rule_id": "BROKEN", "covers_fields": {"Query.ingeschrevenPersoon"}, "deny": "not a set"},
	"sound": {"rule_id": "SOUND", "covers_fields": {"BelastingjaarAangifte.belastingjaar"}, "deny": set()},
}}

test_a_field_without_a_decision_is_refused_not_dropped if {
	req := fx.request(fx.hv, "bri", [fx.person("999991772"), fx.year], {})
	result := gbo.response with input as req with data.gbo.rules as _broken_and_sound_rules
	result.decision == false
	fx.client(result) == {"code": "ACCESS_DENIED"}
	fx.request_code(result) == {"code": "CONFIG_ERROR", "subcode": "RULE_NOT_EVALUABLE"}
	[[d.index, d.code] | some d in fx.denied(result)] == [[0, "RULE_NOT_EVALUABLE"]]
}
