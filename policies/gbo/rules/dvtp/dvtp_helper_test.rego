package gbo.rules.dvtp.helper_test

import data.gbo.fixtures_test as fx
import data.gbo.rules.dvtp.helper

_consent := {"context_valid": true, "status_available": true, "exists": true, "withdrawn": false, "granted_scopes": ["bd:ib:2025"], "valid_until": "2030-01-01T00:00:00Z", "dienstverlener_oin": fx.hv}

_request := fx.income(fx.hv, "consent:identity", [2025], fx.headers)

_both := {"consent:identity", "consent:pseudonym"}

_names(bsn, consent) if {
	helper.names_consent_citizen(fx.person(bsn), "bsn", _both) with input as _request
		with data.gbo.consent.resolved as consent
}

# ── The consent is the one the policy verified ──────────────────────────

test_consent_is_the_resolved_one if {
	c := helper.consent with input as _request with data.gbo.consent.resolved as _consent
	c == _consent
}

# A consent the request carries itself is not one the policy verified.
test_consent_in_the_request_is_not_trusted if {
	req := object.union(_request, {"context": {"pip": {"consent": _consent}}})
	c := helper.consent with input as req
	c == {}
}

# ── A placeholder stands for the citizen of a verified consent ──────────

test_both_placeholders_stand_for_the_citizen if {
	every placeholder in _both {
		_names(placeholder, _consent)
	}
}

test_placeholder_names_nobody_without_consent if {
	every placeholder in _both {
		not helper.names_consent_citizen(fx.person(placeholder), "bsn", _both) with input as _request
	}
}

test_placeholder_names_nobody_under_an_unverified_consent if {
	every placeholder in _both {
		not _names(placeholder, {"context_valid": false, "invalid_code": "CONSENT_SIGNATURE_INVALID"})
	}
}

# The policy decides which placeholders exist; a request cannot add its own.
test_placeholders_are_not_taken_from_the_request if {
	req := object.union(_request, {"context": {"resource": {"subject_placeholders": ["999991772"]}}})
	not helper.names_consent_citizen(fx.person("999991772"), "bsn", _both) with input as req
		with data.gbo.consent.resolved as _consent
}
