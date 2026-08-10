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

# Regression: the BRP attestation uses its own flow key so the request
# mapper can resolve the BRP GraphQL schema. It must still dispatch to
# EUD0002 instead of falling through to NO_APPLICABLE_RULE.
_eudi_brp_input := {
	"subject": {"type": "org", "id": "0000009961MINEZK0000"},
	"context": {
		"flow": "eudi:attestation:brp",
		"time": "2026-08-04T07:13:33Z",
		"resource": {
			"scope": "brp:akte:overlijden",
			"variables": {"bsn": "999991772"},
		},
		"pip": {"pid": {"pi": "PI-70e1c7effa589c71"}},
		"resolved": {
			"coverage_unverifiable": false,
			"args": {"vars.bsn": "999991772"},
			"fields": [{
				"id": "Query.ingeschrevenPersoon.heeftHuwelijk",
				"parent": "IngeschrevenPersoon",
				"name": "heeftHuwelijk",
				"scalar": false,
				"known": true,
			}],
		},
	},
}

test_eudi_brp_flow_dispatches_to_eud0002 if {
	result := gbo.response with input as _eudi_brp_input
	result.decision == true
	result.context.granted[0].rule == "EUD0002"
}
