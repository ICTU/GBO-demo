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
