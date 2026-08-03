package dvtp.gbo_test

import data.dvtp.gbo

# Engine-level ctx-shape: consent is retrieved during evaluation
# (consent.rego), and the engine mirrors its pi onto ctx.resource for the
# constraint-binding rule. http.send is mocked throughout — a policy test
# must not depend on a reachable consent-register.

_consent_record := {
	"status": "ACTIVE",
	"scopes": ["bd:ib:2025"],
	"valid_until": "2030-01-01T00:00:00Z",
	"pi": "PI-abc123",
}

_ok_response := {"status_code": 200, "body": [_consent_record]}

_input := {
	"subject": {"type": "org", "id": "peer-oin-123"},
	"context": {
		"flow": "dvtp:query",
		"resource": {"scope": "bd:ib:2025", "variables": {"bsn": "PI-abc123"}},
	},
}

test_ctx_consent_from_pip_lookup if {
	ctx := gbo._ctx with input as _input with http.send as _ok_response
	ctx.pip.consent.exists == true
	ctx.pip.consent.pi == "PI-abc123"
	ctx.pip.consent.withdrawn == false
	ctx.pip.consent.granted_scopes == ["bd:ib:2025"]
}

test_ctx_resource_pi_mirror if {
	ctx := gbo._ctx with input as _input with http.send as _ok_response
	ctx.resource.pi == "PI-abc123"
}

test_ctx_resource_pi_empty_without_consent if {
	ctx := gbo._ctx with input as {"subject": {"type": "org", "id": "x"}, "context": {}}
	ctx.resource.pi == ""
}

# A revoked record still comes back, so DVT0001 can deny with
# CONSENT_WITHDRAWN rather than the blunter CONSENT_NOT_FOUND.
test_revoked_consent_reported_as_withdrawn if {
	revoked := object.union(_consent_record, {"status": "REVOKED"})
	ctx := gbo._ctx with input as _input
		with http.send as {"status_code": 200, "body": [revoked]}

	ctx.pip.consent.exists == true
	ctx.pip.consent.withdrawn == true
}

# An ACTIVE record wins over a revoked sibling for the same PI.
test_active_preferred_over_revoked if {
	revoked := object.union(_consent_record, {"status": "REVOKED", "pi": "PI-revoked"})
	ctx := gbo._ctx with input as _input
		with http.send as {"status_code": 200, "body": [revoked, _consent_record]}

	ctx.pip.consent.withdrawn == false
	ctx.pip.consent.pi == "PI-abc123"
}

# Fail-closed: an unreachable or erroring register denies, it does not
# leave the rule undefined and it does not fabricate a consent.
test_consent_lookup_failure_is_not_found if {
	ctx := gbo._ctx with input as _input
		with http.send as {"status_code": 503, "body": {}}

	ctx.pip.consent.exists == false
}

test_consent_empty_register_is_not_found if {
	ctx := gbo._ctx with input as _input with http.send as {"status_code": 200, "body": []}
	ctx.pip.consent.exists == false
}

# EUDI discloses a PID and needs no consent; no lookup is attempted, so a
# raising http.send would fail the test if one were made.
test_no_consent_lookup_for_eudi_flow if {
	eudi := object.union(_input, {"context": object.union(_input.context, {"flow": "eudi:attestation:brp"})})
	ctx := gbo._ctx with input as eudi with http.send as _ok_response
	ctx.pip.consent.exists == false
}

# ── Flow dispatch ───────────────────────────────────────────────────────────
# Regression: the flow string carries the bronprofiel, so matching it for
# equality against "eudi:attestation" silenced EUD0002 for every
# akte-van-overlijden request — the rule never fired and the engine denied
# with NO_APPLICABLE_RULE on a query it was written to cover.

test_pid_rule_applies_to_brp_flow if {
	gbo._flow_applicable("EUD0002") with input as {"context": {"flow": "eudi:attestation:brp"}}
}

test_pid_rule_applies_to_base_eudi_flow if {
	gbo._flow_applicable("EUD0001") with input as {"context": {"flow": "eudi:attestation"}}
}

test_consent_rule_not_applicable_under_eudi_flow if {
	not gbo._flow_applicable("DVT0001") with input as {"context": {"flow": "eudi:attestation:brp"}}
}

test_pid_rule_not_applicable_under_dvtp_flow if {
	not gbo._flow_applicable("EUD0001") with input as {"context": {"flow": "dvtp:query"}}
}

# No flow claim in the FSC token: the mapper no longer defaults to
# dvtp:query, so nothing dispatches and the closed-world engine denies.
test_absent_flow_matches_no_rule if {
	not gbo._flow_applicable("DVT0001") with input as {"context": {}}
	not gbo._flow_applicable("EUD0001") with input as {"context": {}}
	not gbo._flow_applicable("EUD0002") with input as {"context": {}}
}
