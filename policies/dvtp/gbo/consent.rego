package dvtp.gbo.consent

# Consent PIP: verifies the citizen's consent token on the request and asks
# the consent register whether that consent is still ACTIVE, while the policy
# evaluates.
#
# The status is fetched with http.send on every evaluation, not through
# OpenFTV's network PIP, which refreshes on an interval: a revoked consent
# must deny on the very next request. It is asked over FSC through this PDP's
# Outway, so the register answers only a peer the Inway authenticated. The
# JWKS is public and fetched from the register directly.
#
# No failure leaves `resolved` undefined: every http.send has a timeout and
# raise_error false, and every outcome maps to a reason code.
#
# What the register answered is not in input, so the decision log cannot show
# it; the engine copies the resolved consent into its response for that reason.

_token_type := "gbo-consent+jwt"

_clock_skew_ns := 30 * 1000000000

_timeout := "2s"

# Keys may be cached; a status may not.
_jwks_cache_seconds := 300

_env := object.get(opa.runtime(), "env", {})

_setting(name, fallback) := value if {
	value := _env[name]
	is_string(value)
	value != ""
} else := fallback

# From the PDP's environment, defaulting to the demo deployment.
config := {
	"url": _setting("GBO_CONSENT_URL", "http://consent-register:4002"),
	"status_url": _setting("GBO_CONSENT_STATUS_URL", "http://pdp-outway:8080/consent-status"),
	"issuer": _setting("GBO_CONSENT_ISSUER", "https://consent-register.gbo.test"),
	"audience": _setting("GBO_CONSENT_AUDIENCE", "gbo:dvtp:pdp"),
}

# ── The token ───────────────────────────────────────────────────────────────
# X-GBO-Consent-Token, in any header-name case. Two tokens are rejected, not
# resolved by picking one: which consent was judged would be ambiguous.

_token_values contains value if {
	some name, value in object.get(input.context, "headers", {})
	lower(name) == "x-gbo-consent-token"
	is_string(value)
	value != ""
}

# Without a token the request is not under the consent regime and `resolved`
# stays undefined.
present if count(_token_values) > 0

token := t if {
	count(_token_values) == 1
	some t in _token_values
}

_decoded := io.jwt.decode(token)

_header := _decoded[0]

_claims := _decoded[1]

# ── Verification keys ──────────────────────────────────────────────────────
# An unknown kid bypasses the cache, so a rotated key is picked up at once.
# No stale fallback: the register that serves the keys also answers the
# status, so an outage denies anyway.

_jwks_request := {
	"method": "GET",
	"url": sprintf("%s/.well-known/jwks.json", [config.url]),
	"timeout": _timeout,
	"raise_error": false,
}

_jwks_cached := http.send(object.union(_jwks_request, {
	"force_cache": true,
	"force_cache_duration_seconds": _jwks_cache_seconds,
}))

_jwks := _jwks_cached if {
	_jwks_cached.status_code == 200
	_key(_jwks_cached.body, _header.kid)
} else := http.send(_jwks_request)

_keys_available if {
	_jwks.status_code == 200
	is_array(_jwks.body.keys)
}

# The key the token names; only ES256 on P-256 is accepted.
_key(jwks, kid) := keys[0] if {
	keys := [k |
		some k in jwks.keys
		k.kid == kid
		k.kty == "EC"
		k.crv == "P-256"
		k.alg == "ES256"
	]
	count(keys) > 0
}

_signature_valid if {
	_header.alg == "ES256"
	key := _key(_jwks.body, _header.kid)
	io.jwt.verify_es256(token, json.marshal(key))
}

# ── Claims ──────────────────────────────────────────────────────────────────

_now_ns := time.parse_rfc3339_ns(input.context.time) if {
	is_string(input.context.time)
	input.context.time != ""
} else := time.now_ns()

_ns(seconds) := seconds * 1000000000

_expired if _now_ns > _ns(_claims.exp) + _clock_skew_ns

_claim_errors contains "unexpected token type" if object.get(_header, "typ", "") != _token_type

_claim_errors contains "issuer mismatch" if object.get(_claims, "iss", "") != config.issuer

_claim_errors contains "audience mismatch" if not _audience_matches

_claim_errors contains "required claims missing" if not _required_claims_present

_claim_errors contains "not yet valid" if _now_ns + _clock_skew_ns < _ns(_claims.nbf)

_claim_errors contains "issued in the future" if _now_ns + _clock_skew_ns < _ns(_claims.iat)

_claim_errors contains "valid_until does not match exp" if not _valid_until_matches_exp

_audience_matches if _claims.aud == config.audience

_audience_matches if {
	is_array(_claims.aud)
	config.audience in _claims.aud
}

_required_claims_present if {
	every name in ["consent_id", "dienstverlener_oin", "valid_until", "jti"] {
		is_string(_claims[name])
		_claims[name] != ""
	}
	is_array(_claims.scopes)

	# Encrypted subject per party: always a pseudonym, plus an identity for a
	# party that may receive the BSN. The policy does not read them, but a
	# token without any could never be answered by a source.
	is_object(_claims.encrypted_subject)
	count(_claims.encrypted_subject) > 0
	every _, party in _claims.encrypted_subject {
		is_object(party.pseudonym)
	}
	every name in ["iat", "nbf", "exp"] {
		is_number(_claims[name])
	}
}

_valid_until_matches_exp if time.parse_rfc3339_ns(_claims.valid_until) == _ns(_claims.exp)

# ── Verification outcome ───────────────────────────────────────────────────
# Signature before claims: the claims of an unverified token are not the
# register's, so they are not reasons. The status is asked only after this
# passes, so a forged token never reaches the register.

_verification := _failure("CONSENT_CONTEXT_INVALID", "more than one consent token") if {
	not token
} else := _failure("CONSENT_SIGNATURE_INVALID", "token is not a JWT") if {
	not _claims
} else := _failure("CONSENT_KEYS_UNAVAILABLE", "verification keys unavailable") if {
	not _keys_available
} else := _failure("CONSENT_SIGNATURE_INVALID", "signature does not verify against the register's key") if {
	not _signature_valid
} else := _failure("CONSENT_TOKEN_EXPIRED", "token has expired") if {
	_expired
} else := _failure("CONSENT_CONTEXT_INVALID", concat(", ", sort(_claim_errors))) if {
	count(_claim_errors) > 0
} else := {"code": "", "reason": ""}

_failure(code, reason) := {"code": code, "reason": reason}

# ── Status ──────────────────────────────────────────────────────────────────
# Uncached. The register logs each lookup as a Dataverwerking, so the request
# carries the caller's trace (with a span of its own) and transaction id;
# otherwise that record would land outside the request it belongs to.
_status_request := {
	"method": "GET",
	"url": sprintf("%s/consents/%s/status", [config.status_url, urlquery.encode(_claims.consent_id)]),
	"timeout": _timeout,
	"raise_error": false,
	"headers": object.union(_transaction_header, _traceparent_header),
}

_transaction_header := {"Fsc-Transaction-Id": _transaction_id} if {
	_transaction_id
} else := {}

# The FSC transaction id, else X-Request-ID. Two values are not resolved by
# picking one.
_transaction_id := tx if {
	values := _header_values("fsc-transaction-id")
	count(values) == 1
	some tx in values
} else := tx if {
	values := _header_values("x-request-id")
	count(values) == 1
	some tx in values
}

_header_values(lower_name) := {v |
	some name, v in object.get(input.context, "headers", {})
	lower(name) == lower_name
	is_string(v)
	v != ""
}

_traceparent_header := {"traceparent": sprintf("00-%s-%s-01", [_trace_id, _lookup_span])} if {
	_trace_id
} else := {}

# The caller's traceparent, else the transaction id (the chain's entry ties
# its trace to it). Undefined when neither is valid: a malformed traceparent
# is worse than none.
_trace_id := id if {
	tp := _incoming_traceparent
	count(tp) >= 55
	substring(tp, 2, 1) == "-"
	substring(tp, 35, 1) == "-"
	id := lower(substring(tp, 3, 32))
	_valid_trace_id(id)
} else := id if {
	id := lower(replace(_transaction_id, "-", ""))
	_valid_trace_id(id)
}

# The Inway passes it among the headers; OpenFTV's own PEP puts it in
# context.traceparent.
_incoming_traceparent := values[0] if {
	values := [v |
		some name, v in object.get(input.context, "headers", {})
		lower(name) == "traceparent"
		is_string(v)
	]
	count(values) > 0
} else := tp if {
	tp := input.context.traceparent
	is_string(tp)
}

# 32 lowercase hex characters, not all zero (invalid in W3C Trace Context).
_valid_trace_id(id) if {
	regex.match(`^[0-9a-f]{32}$`, id)
	id != "00000000000000000000000000000000"
}

# A fresh span id; uuid.rfc4122 is random per evaluation, the argument only
# names the value.
_lookup_span := substring(replace(uuid.rfc4122("consent-status-span"), "-", ""), 0, 16)

_status_response := http.send(_status_request)

# Only ACTIVE and REVOKED count as answers, and a 404 means the register does
# not know the consent. Anything else (a mismatched consent_id, a timeout, an
# error) makes the status unavailable, which denies.
_status := {"available": true, "exists": true, "withdrawn": status == "REVOKED"} if {
	_status_response.status_code == 200
	_status_response.body.consent_id == _claims.consent_id
	status := _status_response.body.status
	status in {"ACTIVE", "REVOKED"}
} else := {"available": true, "exists": false, "withdrawn": false} if {
	_status_response.status_code == 404
} else := {"available": false, "exists": false, "withdrawn": false}

# ── pip.consent ─────────────────────────────────────────────────────────────
# The shape the rule cascade reads; invalid_code becomes the deny reason.

resolved := {
	"context_valid": false,
	"status_available": false,
	"exists": false,
	"invalid_code": _verification.code,
	"invalid_reason": _verification.reason,
} if {
	present
	_verification.code != ""
} else := {
	"context_valid": true,
	"status_available": _status.available,
	"exists": _status.exists,
	"withdrawn": _status.withdrawn,
	"granted_scopes": _claims.scopes,
	"valid_until": _claims.valid_until,
	"dienstverlener_oin": _claims.dienstverlener_oin,
	"consent_id": _claims.consent_id,
	"jti": _claims.jti,
} if {
	present
}
