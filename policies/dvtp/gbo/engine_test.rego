package dvtp.gbo_test

import data.dvtp.gbo

# Engine-level consent lookup: the OpenFTV PIP pull lands ACTIVE
# consents in data.attributes.consents; the engine matches by PI from
# resource.variables.bsn and mirrors pi onto ctx.resource for the
# constraint-binding rule.

_consents := [{"pi": "PI-abc123", "scopes": ["bd:ib:2025"], "valid_until": "2030-01-01T00:00:00Z"}]

_input := {"context": {"resource": {"variables": {"bsn": "PI-abc123"}}}}

test_consent_match_by_pi if {
	c := gbo._consent_match with data.attributes as {"consents": _consents}
		with input as _input
	c.pi == "PI-abc123"
}

test_consent_found_true if {
	gbo._consent_found with data.attributes as {"consents": _consents}
		with input as _input
}

test_consent_not_found_when_absent if {
	not gbo._consent_found with data.attributes as {"consents": []}
		with input as _input
}

test_pip_consent_shape if {
	pip := gbo._pip_consent with data.attributes as {"consents": _consents}
		with input as _input
	pip.exists == true
	pip.withdrawn == false
	pip.granted_scopes == ["bd:ib:2025"]
	pip.valid_until == "2030-01-01T00:00:00Z"
	pip.pi == "PI-abc123"
}

test_ctx_resource_pi_mirror if {
	ctx := gbo._ctx with data.attributes as {"consents": _consents}
		with input as _input
	ctx.resource.pi == "PI-abc123"
}
