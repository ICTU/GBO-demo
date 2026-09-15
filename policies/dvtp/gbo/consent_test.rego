package dvtp.gbo.consent_test

import data.dvtp.gbo
import data.dvtp.gbo.consent

# ═══════════════════════════════════════════════════════════════════════════
# The consent PIP end to end: real ES256 tokens, the register mocked at
# http.send. Tests that go through gbo.response assert the reason a PEP would
# see; tests that read consent.resolved assert the attribute itself.
# ═══════════════════════════════════════════════════════════════════════════

# Test-only P-256 keys, generated for this file. _register_key stands in for
# the consent register's signing key.
_register_key := {
	"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig",
	"kid": "gbo-consent-demo-1",
	"x": "4O4Kt3X3NCcX_gQM8J3X0aGLkyP9HzBfZCE_OM9AsME",
	"y": "zUlXMsrXeNjr66eKLtn1XI4HUEzO8st-RHWCLJaDjwY",
	"d": "GEiyqKchkG7x9jVF8gyCx4eYPUF7DyajUHW9HwNxqjw",
}

# Any other key, naming the register's kid: a forgery claiming the real key.
_forger_key := object.union(_register_key, {
	"x": "1ju7YaeBZhAiBBy60y9rvBlk1tJzMa4C-3KgTmNqPRw",
	"y": "3vSssBZ_msJpVXUnI9eA6bj8NgHIUCSBOri6BmwjjEA",
	"d": "9JUBrfnzt3Uc7txJ2qxyeJ-H-KpP18qXZfPjDPeUz98",
})

_jwks := {"keys": [object.remove(_register_key, ["d"])]}

_register := "http://consent-register:4002"

# The runtime environment, pinned: tests use the defaults, whatever the
# shell running them exports.
_env := {"env": {}}

# 2026-07-06T12:00:00Z, the evaluation time every input carries.
_now := 1783339200

_header := {"alg": "ES256", "typ": "gbo-consent+jwt", "kid": "gbo-consent-demo-1"}

_claims_expiring(exp) := {
	"iss": "https://consent-register.gbo.test",
	"aud": ["gbo:dvtp:pdp"],
	"iat": _now - 60,
	"nbf": _now - 60,
	"exp": exp,
	"valid_until": time.format(exp * 1000000000),
	"jti": "jti-1",
	"consent_id": "c-signed",
	"pi": "PI-abc123",
	"scopes": ["bd:ib:2025"],
	"dienstverlener_oin": "99999999900000000300",
}

_claims := _claims_expiring(_now + 3600)

_sign(header, claims) := io.jwt.encode_sign(header, claims, _register_key)

_token := _sign(_header, _claims)

_box1 := [{"id": "aangifte.box1", "parent": "AangifteIH", "name": "box1Inkomen", "scalar": false}]

_request(headers) := {
	"subject": {"type": "org", "id": "99999999900000000300"},
	"context": {
		"time": "2026-07-06T12:00:00Z",
		"trace_id": "tx-330",
		"headers": headers,
		"resource": {"scope": "bd:ib:2025"},
		"resolved": {"fields": _box1, "args": {"bsn": "PI-abc123", "belastingjaren.0": "2025"}},
	},
}

_input(token) := _request({"X-Gbo-Consent-Token": token})

# ── The register, mocked at http.send ──────────────────────────────────────
# Every mock answers only a well-formed request: an explicit timeout and
# raise_error false. The status endpoint answers only an uncached request.
# A policy that dropped either would get no answer, and the ALLOW test
# below would fail — that is what pins "no cache in the revocation path".

_well_formed(req) if {
	req.timeout != ""
	req.raise_error == false
}

_uncached(req) if {
	not req.cache
	not req.force_cache
}

_keys(req) := {"status_code": 200, "body": _jwks} if {
	_well_formed(req)
	req.url == sprintf("%s/.well-known/jwks.json", [_register])
}

_status(req, status) := {"status_code": 200, "body": {"consent_id": "c-signed", "status": status}} if {
	_well_formed(req)
	_uncached(req)
	req.url == sprintf("%s/consents/c-signed/status", [_register])
}

_network_error := {"status_code": 0, "error": {"code": "eval_http_send_network_error", "message": "dial tcp: connection refused"}}

_register_active(req) := _keys(req)

_register_active(req) := _status(req, "ACTIVE")

_register_revoked(req) := _keys(req)

_register_revoked(req) := _status(req, "REVOKED")

_register_unknown_status(req) := _keys(req)

_register_unknown_status(req) := _status(req, "SUSPENDED")

_register_consent_unknown(req) := _keys(req)

_register_consent_unknown(req) := {"status_code": 404, "body": {"error": "consent not found"}} if {
	req.url == sprintf("%s/consents/c-signed/status", [_register])
}

# The register is gone: every call fails at the network.
_register_down(_) := _network_error

# The keys answer — as they would from cache — and the status call fails.
_register_status_down(req) := _keys(req)

_register_status_down(req) := _network_error if endswith(req.url, "/status")

# The resolved consent and the decision, for one token against one register.
_on_active(token) := r if {
	r := {"consent": consent.resolved, "response": gbo.response} with input as _input(token) with http.send as _register_active with opa.runtime as _env
}

_on_revoked(token) := r if {
	r := {"consent": consent.resolved, "response": gbo.response} with input as _input(token) with http.send as _register_revoked with opa.runtime as _env
}

_on_unknown_status(token) := r if {
	r := {"consent": consent.resolved, "response": gbo.response} with input as _input(token) with http.send as _register_unknown_status with opa.runtime as _env
}

_on_consent_unknown(token) := r if {
	r := {"consent": consent.resolved, "response": gbo.response} with input as _input(token) with http.send as _register_consent_unknown with opa.runtime as _env
}

_on_down(token) := r if {
	r := {"consent": consent.resolved, "response": gbo.response} with input as _input(token) with http.send as _register_down with opa.runtime as _env
}

_on_status_down(token) := r if {
	r := {"consent": consent.resolved, "response": gbo.response} with input as _input(token) with http.send as _register_status_down with opa.runtime as _env
}

_reason(r) := r.response.context.reason_admin.code

# ── Active consent ──────────────────────────────────────────────────────────

test_active_consent_allows if {
	r := _on_active(_token)
	r.response.decision == true
	r.response.context.granted[0].rule == "DVT0001"
}

test_active_consent_resolves_the_signed_claims if {
	c := _on_active(_token).consent
	c.context_valid == true
	c.status_available == true
	c.exists == true
	c.withdrawn == false
	c.pi == "PI-abc123"
	c.dienstverlener_oin == "99999999900000000300"
	c.granted_scopes == ["bd:ib:2025"]
	c.consent_id == "c-signed"
}

# What the decision was taken on reaches the decision log, through the
# response document — it is not in input.
test_response_carries_the_resolved_consent if {
	r := _on_active(_token)
	r.response.context.pip.consent == r.consent
}

# ── Revocation ──────────────────────────────────────────────────────────────

test_revoked_consent_denies_withdrawn if {
	_reason(_on_revoked(_token)) == "CONSENT_WITHDRAWN"
}

# Same token, one evaluation apart: the register's answer is read afresh.
test_revocation_takes_effect_on_the_next_request if {
	_on_active(_token).response.decision == true
	_on_revoked(_token).response.decision == false
}

# The structural half: nothing in the status request lets http.send serve
# it from cache, and a failure yields a response rather than an error.
test_status_request_is_uncached_and_bounded if {
	req := consent._status_request with input as _input(_token) with opa.runtime as _env
	count({"cache", "force_cache"} & object.keys(req)) == 0
	req.raise_error == false
	req.timeout == "2s"
}

test_status_request_carries_the_transaction_id if {
	req := consent._status_request with input as _input(_token) with opa.runtime as _env
	req.headers["Fsc-Transaction-Id"] == "tx-330"
}

# ── Trace context on the status request (#365) ─────────────────────────────
# The register's status record belongs to the request's trace: the caller's
# when it came along, otherwise the transaction id. The span is the lookup's
# own, so the record hangs under it rather than under the caller's span.

_callers_trace := "4bf92f3577b34da6a3ce929d0e0e4736"

_callers_span := "00f067aa0ba902b7"

_traced(headers, context) := object.union(_request(headers), {"context": context})

_status_traceparent(req) := tp if {
	status_request := consent._status_request with input as req with opa.runtime as _env
	tp := status_request.headers.traceparent
}

_is_traceparent(tp) if regex.match(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`, tp)

test_status_request_takes_the_callers_trace if {
	tp := _status_traceparent(_request({
		"X-Gbo-Consent-Token": _token,
		"Traceparent": sprintf("00-%s-%s-01", [_callers_trace, _callers_span]),
	}))
	_is_traceparent(tp)
	substring(tp, 3, 32) == _callers_trace
	substring(tp, 36, 16) != _callers_span
}

test_status_request_takes_the_traceparent_openftv_lifted if {
	tp := _status_traceparent(_traced(
		{"X-Gbo-Consent-Token": _token},
		{"traceparent": sprintf("00-%s-%s-01", [_callers_trace, _callers_span])},
	))
	substring(tp, 3, 32) == _callers_trace
}

test_status_request_falls_back_to_the_transaction_id if {
	tp := _status_traceparent(_traced(
		{"X-Gbo-Consent-Token": _token},
		{"trace_id": "0af76519-16cd-43dd-8448-eb211c80319c"},
	))
	_is_traceparent(tp)
	substring(tp, 3, 32) == "0af7651916cd43dd8448eb211c80319c"
}

# No usable trace — a malformed traceparent, a transaction id that is not a
# trace id — sends none rather than a malformed one.
test_status_request_without_a_usable_trace_carries_none if {
	req := consent._status_request with input as _request({"X-Gbo-Consent-Token": _token, "traceparent": "garbage"})
		with opa.runtime as _env
	not req.headers.traceparent
	req.headers["Fsc-Transaction-Id"] == "tx-330"
}

test_unknown_consent_denies_not_found if {
	_reason(_on_consent_unknown(_token)) == "CONSENT_NOT_FOUND"
}

# ── An unreachable or unhelpful register denies with a reason ──────────────

test_unreachable_register_denies_with_keys_unavailable if {
	r := _on_down(_token)
	r.response.decision == false
	_reason(r) == "CONSENT_KEYS_UNAVAILABLE"
}

test_status_unreachable_denies_with_status_unavailable if {
	r := _on_status_down(_token)
	r.consent.context_valid == true
	_reason(r) == "CONSENT_STATUS_UNAVAILABLE"
}

test_unexpected_status_denies_with_status_unavailable if {
	_reason(_on_unknown_status(_token)) == "CONSENT_STATUS_UNAVAILABLE"
}

# ── A token the register did not sign ──────────────────────────────────────

test_forged_signature_denies if {
	forged := io.jwt.encode_sign(_header, _claims, _forger_key)
	_reason(_on_active(forged)) == "CONSENT_SIGNATURE_INVALID"
}

# The register's signature over other claims: here, another citizen's PI.
test_tampered_claims_deny if {
	[header, _, signature] := split(_token, ".")
	payload := base64url.encode_no_pad(json.marshal(object.union(_claims, {"pi": "PI-2f1a7c9b40e6d853"})))
	_reason(_on_active(concat(".", [header, payload, signature]))) == "CONSENT_SIGNATURE_INVALID"
}

test_unknown_key_denies if {
	token := _sign(object.union(_header, {"kid": "retired-key"}), _claims)
	_reason(_on_active(token)) == "CONSENT_SIGNATURE_INVALID"
}

# Algorithm confusion: an HMAC token keyed with the register's public key.
test_symmetric_algorithm_denies if {
	token := io.jwt.encode_sign(object.union(_header, {"alg": "HS256"}), _claims, {"kty": "oct", "k": _register_key.x})
	_reason(_on_active(token)) == "CONSENT_SIGNATURE_INVALID"
}

test_not_a_jwt_denies if {
	_reason(_on_active("not-a-jwt")) == "CONSENT_SIGNATURE_INVALID"
}

# ── Time claims ─────────────────────────────────────────────────────────────

test_expired_token_denies_token_expired if {
	token := _sign(_header, _claims_expiring(_now - 3600))
	_reason(_on_active(token)) == "CONSENT_TOKEN_EXPIRED"
}

# Within the 30s leeway the token still verifies, and the consent's own
# validity window denies it instead.
test_expiry_within_clock_skew_verifies if {
	r := _on_active(_sign(_header, _claims_expiring(_now - 20)))
	r.consent.context_valid == true
	_reason(r) == "CONSENT_EXPIRED"
}

test_not_before_within_clock_skew_verifies if {
	token := _sign(_header, object.union(_claims, {"nbf": _now + 20, "iat": _now + 20}))
	_on_active(token).consent.context_valid == true
}

test_not_before_beyond_clock_skew_denies if {
	token := _sign(_header, object.union(_claims, {"nbf": _now + 60}))
	c := _on_active(token).consent
	c.invalid_code == "CONSENT_CONTEXT_INVALID"
	contains(c.invalid_reason, "not yet valid")
}

# ── Other claims ────────────────────────────────────────────────────────────

test_claim_violations_deny_context_invalid if {
	cases := {
		"wrong issuer": [_header, object.union(_claims, {"iss": "https://elsewhere.test"})],
		"wrong audience": [_header, object.union(_claims, {"aud": ["gbo:other"]})],
		"wrong type": [object.union(_header, {"typ": "JWT"}), _claims],
		"missing pi": [_header, object.remove(_claims, ["pi"])],
		"missing jti": [_header, object.remove(_claims, ["jti"])],
		"missing scopes": [_header, object.remove(_claims, ["scopes"])],
		"valid_until differs from exp": [_header, object.union(_claims, {"valid_until": "2030-01-01T00:00:00Z"})],
	}
	every _, c in cases {
		_reason(_on_active(_sign(c[0], c[1]))) == "CONSENT_CONTEXT_INVALID"
	}
}

test_single_audience_string_verifies if {
	token := _sign(_header, object.union(_claims, {"aud": "gbo:dvtp:pdp"}))
	_on_active(token).consent.context_valid == true
}

# ── Finding the token ───────────────────────────────────────────────────────

test_header_name_is_case_insensitive if {
	c := consent.resolved with input as _request({"x-gbo-consent-token": _token}) with http.send as _register_active with opa.runtime as _env
	c.context_valid == true
}

test_two_tokens_are_not_resolved_by_picking_one if {
	c := consent.resolved with input as _request({"X-Gbo-Consent-Token": _token, "x-gbo-consent-token": "ey.other.token"}) with http.send as _register_active with opa.runtime as _env
	c.invalid_code == "CONSENT_CONTEXT_INVALID"
}

test_no_token_resolves_no_consent if {
	not consent.resolved with input as _request({}) with http.send as _register_active with opa.runtime as _env
}

test_empty_token_is_not_consent_evidence if {
	not consent.resolved with input as _request({"X-Gbo-Consent-Token": ""}) with http.send as _register_active with opa.runtime as _env
}

# ── Configuration ───────────────────────────────────────────────────────────

test_register_location_is_operator_configuration if {
	cfg := consent.config with opa.runtime as {"env": {
		"GBO_CONSENT_URL": "http://register.example:8080",
		"GBO_CONSENT_AUDIENCE": "aud-x",
	}}
	cfg.url == "http://register.example:8080"
	cfg.audience == "aud-x"
	cfg.issuer == "https://consent-register.gbo.test"
}
