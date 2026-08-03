package dvtp.gbo

# ═══════════════════════════════════════════════════════════════════════════
# Consent PIP — retrieved by the PDP during evaluation.
#
# The FTV standard puts attribute retrieval in the PDP ("the PDP retrieves
# applicable policies from the PAP and any additional attributes from the
# PIP"), so the consent lookup lives here rather than in the request-mapper
# that reshapes the incoming request. Two practical consequences:
#
#   - a revoke takes effect on the next request, with no refresh interval
#     to wait out. OpenFTV's configured PIP cannot do this: its network PIP
#     is a scheduled loader, and the PDP's attribute store rejects every
#     update after the first create (see ICTU-37 items 5 and 6), so it can
#     hold no attribute that changes.
#   - the mapper loses the last piece of GBO-specific machinery it carried:
#     our register's URL shape and response model.
#
# Consent applies to the DvTP flow only. EUDI and OOTS establish their
# access basis differently (a disclosed PID, resp. an OOTS evidence
# request), so no lookup is made for them.
#
# Fail-closed: any error — unreachable register, non-200, undecodable body,
# no record — yields exists=false, which DVT0001 denies as
# CONSENT_NOT_FOUND. raise_error=false keeps a network failure from
# collapsing the whole rule into "undefined" (which would deny without a
# reason code).
# ═══════════════════════════════════════════════════════════════════════════

_consent_not_found := {"exists": false}

# The subject identifier travels as the `bsn` query-variable. In the DvTP
# flow both sides of the constraint-binding are already PI pseudonyms —
# the plain BSN is resolved source-side by the bron-sidecar, after this
# decision.
_consent_pi := pi if {
	pi := object.get(object.get(input.context, "resource", {}), "variables", {}).bsn
	is_string(pi)
} else := ""

_consent_scope := object.get(object.get(input.context, "resource", {}), "scope", "")

_consent_url := u if {
	u := opa.runtime().env.GBO_CONSENT_URL
	u != ""
} else := "http://consent-register:4002"

# consent_pip is the {exists, withdrawn, granted_scopes, valid_until, pi}
# shape lib.evaluate's consent-axes read.
default consent_pip := {"exists": false}

consent_pip := _consent_from(_consent_records) if {
	input.context.flow == "dvtp:query"
	_consent_pi != ""
	count(_consent_records) > 0
}

_consent_records := records if {
	resp := http.send({
		"method": "GET",
		"url": sprintf(
			"%s/consents?pi=%s&scope=%s",
			[_consent_url, urlquery.encode(_consent_pi), urlquery.encode(_consent_scope)],
		),
		"timeout": "2s",
		"raise_error": false,
		"force_json_decode": true,
		# Identical lookups within one evaluation hit the intra-query
		# cache, so per-field evaluation does not fan out into N requests.
		"cache": true,
	})
	resp.status_code == 200
	is_array(resp.body)
	records := resp.body
} else := []

# Prefer an ACTIVE record; otherwise report the revoked one, so the policy
# can deny with CONSENT_WITHDRAWN rather than the blunter NOT_FOUND.
_consent_from(records) := _consent_shape(active[0]) if {
	active := [r | some r in records; r.status == "ACTIVE"]
	count(active) > 0
} else := _consent_shape(records[0])

_consent_shape(r) := {
	"exists": true,
	"withdrawn": object.get(r, "status", "") == "REVOKED",
	"granted_scopes": object.get(r, "scopes", []),
	"valid_until": object.get(r, "valid_until", ""),
	"pi": object.get(r, "pi", ""),
}
