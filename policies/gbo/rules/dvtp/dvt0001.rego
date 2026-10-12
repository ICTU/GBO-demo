package gbo.rules.dvtp.dvt0001

# DVT0001 — Income data under a citizen's consent.
#
# Grants a service provider the income declaration fields when the citizen's
# consent is valid, not withdrawn or expired, given to that provider, and
# covers the scope and every requested year. The query names its subject with
# the identity placeholder, so the subject can only be the consent's citizen.

import data.gbo.lib
import data.gbo.rules.dvtp.helper

rule_id := "DVT0001"

# Bound only when a consent token comes along; without one the EUDI rules
# judge the request. This decides whose reasons are reported, not access:
# without a verified token the consent checks below deny anyway.
regime := "consent"

# Bedrag's scalars (waarde, valuta) are covered through their type. The
# declaration types are listed field by field instead, so box2Inkomen and
# box3Inkomen stay uncovered here; under consent no rule covers them, and
# they are denied with NO_APPLICABLE_RULE.
covers_types := {"Bedrag"}

# Anything not listed is denied by the engine's closed world. Interface fields
# are listed for both BelastingjaarAangifte and AangifteIH: the parent type
# depends on whether the query selects them inside `... on AangifteIH`.
covers_fields := {
	# the root field, which names the subject, and the object-edges
	"Query.ingeschrevenPersoon",
	"IngeschrevenPersoon.heeftBelastingjaarAangifte",
	"AangifteIH.verzamelinkomen",
	"AangifteIH.box1Inkomen",
	# scalar fields for the mortgage/IB use-case
	"BelastingjaarAangifte.belastingjaar",
	"BelastingjaarAangifte.status",
	"BelastingjaarAangifte.indieningsdatum",
	"AangifteIH.belastingjaar",
	"AangifteIH.status",
	"AangifteIH.indieningsdatum",
}

# ── The consent ──────────────────────────────────────────────────────────────

deny contains helper.consent_invalid_reason if not helper.consent_verified

deny contains "CONSENT_STATUS_UNAVAILABLE" if not helper.consent_status_known

deny contains "CONSENT_NOT_FOUND" if not helper.consent_exists

deny contains "CONSENT_WITHDRAWN" if not helper.consent_active

deny contains "CONSENT_EXPIRED" if helper.consent_expired

deny contains "CONSENT_SCOPE_MISMATCH" if not helper.consent_covers_declared_scope

deny contains "CONSENT_ACTOR_MISMATCH" if not helper.consent_given_to_caller

# ── The question ─────────────────────────────────────────────────────────────

# The root field's bsn must be the identity placeholder: the provider holds
# no identifier of the citizen, and the source puts in the BSN from the
# verified consent token. A literal BSN would be a subject the caller chose.
# The income API takes a BSN, not a pseudonym.
deny_field[i] contains "CONSTRAINT_MISMATCH" if {
	some i, f in lib.fields
	lib.key(f) == "Query.ingeschrevenPersoon"
	not helper.names_consent_citizen(f, "bsn", {lib.identity_placeholder})
}

# Every requested belastingjaar needs a bd:ib:<year> scope in the consent: a
# consent for 2025 only denies a query for 2024 and 2025.
deny_field[i] contains "YEAR_NOT_COVERED" if {
	some i, f in lib.fields
	lib.key(f) == "IngeschrevenPersoon.heeftBelastingjaarAangifte"
	some year in lib.requested_years(f, "belastingjaren")
	not sprintf("bd:ib:%s", [year]) in helper.consent.granted_scopes
}

# No year filter fails too: the source would return every year.
deny_field[i] contains "YEAR_NOT_COVERED" if {
	some i, f in lib.fields
	lib.key(f) == "IngeschrevenPersoon.heeftBelastingjaarAangifte"
	count(lib.requested_years(f, "belastingjaren")) == 0
}
