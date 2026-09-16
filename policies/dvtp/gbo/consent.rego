package dvtp.gbo.consent

# ═══════════════════════════════════════════════════════════════════════════
# Consent PIP: resolved by the policy, per evaluation (#330).
#
# FTV places attribute retrieval inside the PDP — L4, step 3 of the request
# flow: the PDP asks its PIP while it decides. This package is that step for
# the citizen's consent. It reads the signed consent token off the request,
# verifies it against the consent register's JWKS, and asks the register
# whether the consent the token names is still ACTIVE.
#
# The status call is why this is http.send and not OpenFTV's configured
# network PIP: that PIP is a loader on a refresh interval, and a revoked
# consent must deny on the first request after revocation. The status
# request therefore carries no cache directive. OPA does cache identical
# calls within one evaluation, so evaluating many fields costs one call.
#
# No failure leaves a rule undefined. Every http.send runs with an explicit
# timeout and raise_error false, and every outcome maps onto the pip.consent
# shape lib.rego's cascade reads. A token that does not verify carries
# invalid_code, which the cascade reports as the deny reason:
#
#   CONSENT_KEYS_UNAVAILABLE   the register's JWKS could not be fetched
#   CONSENT_SIGNATURE_INVALID  not a JWT, not ES256, unknown key, bad signature
#   CONSENT_TOKEN_EXPIRED      exp has passed, beyond the clock-skew leeway
#   CONSENT_CONTEXT_INVALID    any other claim: typ, iss, aud, nbf, iat,
#                              required claims, valid_until != exp
#
# The status is only asked for once the token verified, so a forged token
# never reaches the register, and never creates a Dataverwerking there.
#
# The keys and the status travel differently (#383). The JWKS is public and
# fetched from the register directly. The status is asked over FSC: through
# this PDP's Outway, under the grant-link for the register's consent-status
# service, and the register answers only a peer the Inway authenticated.
#
# Configuration is the operator's: GBO_CONSENT_URL (the register, for the
# JWKS), GBO_CONSENT_STATUS_URL (the Outway's grant-link), GBO_CONSENT_ISSUER
# and GBO_CONSENT_AUDIENCE from the PDP's environment, defaulting to the demo
# deployment.
#
# The cost, weighed in #330: a decision is no longer a pure function of
# (input, policy, data). What the register answered is not in input, so the
# decision log cannot show it — the engine puts the resolved consent back
# into its response document for that reason. Replaying a decision record
# would need OPA's nd_builtin_cache in the log, which OpenFTV does not enable.
# ═══════════════════════════════════════════════════════════════════════════

_token_type := "gbo-consent+jwt"

_clock_skew_ns := 30 * 1000000000

_timeout := "2s"

# Verification keys may be cached; a key is not a status. See _jwks.
_jwks_cache_seconds := 300

_env := object.get(opa.runtime(), "env", {})

_setting(name, fallback) := value if {
	value := _env[name]
	is_string(value)
	value != ""
} else := fallback

config := {
	"url": _setting("GBO_CONSENT_URL", "http://consent-register:4002"),
	"status_url": _setting("GBO_CONSENT_STATUS_URL", "http://pdp-outway:8080/consent-status"),
	"issuer": _setting("GBO_CONSENT_ISSUER", "https://consent-register.gbo.test"),
	"audience": _setting("GBO_CONSENT_AUDIENCE", "gbo:dvtp:pdp"),
}

# ── The token ───────────────────────────────────────────────────────────────
# X-GBO-Consent-Token, in whatever case the PEP forwarded the header name.
# More than one value is not resolved by picking one.

_token_values contains value if {
	some name, value in object.get(input.context, "headers", {})
	lower(name) == "x-gbo-consent-token"
	is_string(value)
	value != ""
}

# Whether the request carries consent evidence at all. Without a token,
# `resolved` is undefined: the request is not under the consent regime, and
# pip.consent stays absent.
present if count(_token_values) > 0

token := t if {
	count(_token_values) == 1
	some t in _token_values
}

_decoded := io.jwt.decode(token)

_header := _decoded[0]

_claims := _decoded[1]

# ── Verification keys ──────────────────────────────────────────────────────
# The register's JWKS, cached for _jwks_cache_seconds. A kid the cached set
# does not know bypasses the cache, so a rotated key is picked up on first
# sight. There is no stale fallback during an outage: the register that
# serves the keys also answers the status, so an outage denies regardless.

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

# The key the token names. Only ES256 on P-256 is accepted, whatever else
# the set carries.
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
	every name in ["consent_id", "pi", "dienstverlener_oin", "valid_until", "jti"] {
		is_string(_claims[name])
		_claims[name] != ""
	}
	is_array(_claims.scopes)
	every name in ["iat", "nbf", "exp"] {
		is_number(_claims[name])
	}
}

_valid_until_matches_exp if time.parse_rfc3339_ns(_claims.valid_until) == _ns(_claims.exp)

# ── Verification outcome ───────────────────────────────────────────────────
# Signature before claims: the claims of a token that does not verify are
# not the register's, so they are not reasons.

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
# Asked on every evaluation, uncached. Confirming a status is itself a
# Dataverwerking the register logs, so the request carries the trace it
# belongs to (#365, LDV §3.1): a traceparent with the request's trace and a
# span of its own for this lookup, under which the register files its record,
# and the transaction id alongside for the logs that key on it. Without them
# the record lands under a trace of its own — outside the request it was part
# of.

_status_request := {
	"method": "GET",
	"url": sprintf("%s/consents/%s/status", [config.status_url, urlquery.encode(_claims.consent_id)]),
	"timeout": _timeout,
	"raise_error": false,
	"headers": object.union(_transaction_header, _traceparent_header),
}

_transaction_header := {"Fsc-Transaction-Id": tx} if {
	tx := input.context.trace_id
	is_string(tx)
	tx != ""
} else := {}

_traceparent_header := {"traceparent": sprintf("00-%s-%s-01", [_trace_id, _lookup_span])} if {
	_trace_id
} else := {}

# The request's trace: the caller's traceparent, and otherwise the
# transaction id, which the chain's entry ties its trace to. Undefined when
# neither yields a valid trace id; a malformed traceparent is worse than none.
_trace_id := id if {
	tp := _incoming_traceparent
	count(tp) >= 55
	substring(tp, 2, 1) == "-"
	substring(tp, 35, 1) == "-"
	id := lower(substring(tp, 3, 32))
	_valid_trace_id(id)
} else := id if {
	id := lower(replace(input.context.trace_id, "-", ""))
	_valid_trace_id(id)
}

# The Inway copies the caller's traceparent into the AuthZEN context with the
# rest of the headers; OpenFTV's own PEP lifts it into context.traceparent.
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

# 32 lowercase hex characters, and not the all-zero id W3C Trace Context
# declares invalid.
_valid_trace_id(id) if {
	regex.match(`^[0-9a-f]{32}$`, id)
	id != "00000000000000000000000000000000"
}

# A fresh span for the lookup: 16 hex characters of a random UUID.
# uuid.rfc4122 is random per evaluation; the argument only names the value.
_lookup_span := substring(replace(uuid.rfc4122("consent-status-span"), "-", ""), 0, 16)

_status_response := http.send(_status_request)

# ACTIVE and REVOKED are the only statuses with a meaning here. Anything
# else — a mismatched consent_id, a timeout, a register error — leaves the
# status unavailable, which denies (CONSENT_STATUS_UNAVAILABLE). A 404 is an
# answer: the register does not know the consent (CONSENT_NOT_FOUND).
_status := {"available": true, "exists": true, "withdrawn": status == "REVOKED"} if {
	_status_response.status_code == 200
	_status_response.body.consent_id == _claims.consent_id
	status := _status_response.body.status
	status in {"ACTIVE", "REVOKED"}
} else := {"available": true, "exists": false, "withdrawn": false} if {
	_status_response.status_code == 404
} else := {"available": false, "exists": false, "withdrawn": false}

# ── pip.consent ─────────────────────────────────────────────────────────────

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
	"pi": _claims.pi,
	"dienstverlener_oin": _claims.dienstverlener_oin,
	"consent_id": _claims.consent_id,
	"jti": _claims.jti,
} if {
	present
}
