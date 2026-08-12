package dvtp.gbo_test

import data.dvtp.gbo

# Engine-level ctx-shape: the request-mapper places the consent it
# fetched in input.context.pip.consent; the engine mirrors its pi onto
# ctx.resource for the constraint-binding rule.

_pip := {"consent": {"exists": true, "withdrawn": false, "granted_scopes": ["bd:ib:2025"], "valid_until": "2030-01-01T00:00:00Z", "pi": "PI-abc123"}}

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

test_eudi_flow_activates_all_pid_rules if {
	gbo._flow_applicable("EUD0001") with input as {"context": {"flow": "eudi:attestation"}}
	gbo._flow_applicable("EUD0002") with input as {"context": {"flow": "eudi:attestation"}}
}

test_unknown_flow_activates_no_pid_rule if {
	not gbo._flow_applicable("EUD0001") with input as {"context": {"flow": "eudi:other"}}
	not gbo._flow_applicable("EUD0002") with input as {"context": {"flow": "eudi:other"}}
}

_eudi_context(fields, args) := {
	"flow": "eudi:attestation",
	"pip": {"pid": {"pi": "PI-2f1a7c9b40e6d853"}},
	"resolved": {"fields": fields, "args": args},
}

test_generic_eudi_flow_selects_income_rule_by_fields if {
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

test_generic_eudi_flow_selects_brp_rule_by_fields if {
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
