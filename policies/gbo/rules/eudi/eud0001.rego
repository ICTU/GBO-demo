package gbo.rules.eudi.eud0001

# EUD0001 — Income declaration for an EUDI wallet attestation.
#
# Grants a designated issuer the income declaration in the PID regime: no
# consent token, and the BSN the citizen disclosed from the wallet's PID.
# The engine binds it only without a consent token, so on any request at
# most one of DVT0001 and EUD0001 applies, although they share fields.
#
# Not checked here: the PID signature (the adapter trusts the disclosure),
# the wallet certificate and the attestation type.

import data.gbo.lib
import data.gbo.rules.eudi.helper

rule_id := "EUD0001"

regime := "pid"

# DVT0001's fields plus box 2 and box 3.
covers_types := {"Bedrag"}

covers_fields := {
	"Query.ingeschrevenPersoon",
	"IngeschrevenPersoon.heeftBelastingjaarAangifte",
	"BelastingjaarAangifte.belastingjaar",
	"BelastingjaarAangifte.status",
	"BelastingjaarAangifte.indieningsdatum",
	"AangifteIH.belastingjaar",
	"AangifteIH.status",
	"AangifteIH.indieningsdatum",
	"AangifteIH.verzamelinkomen",
	"AangifteIH.box1Inkomen",
	"AangifteIH.box2Inkomen",
	"AangifteIH.box3Inkomen",
}

# Checked against the year filter in the query itself; no scope involved.
allowed_years := {"2024", "2025"}

# ── The caller ───────────────────────────────────────────────────────────────

deny contains "PID_NOT_PRESENT" if helper.consent_token_presented

deny contains "ACTOR_NOT_ALLOWED" if not helper.issuer_designated

# ── The question ─────────────────────────────────────────────────────────────

# The disclosed BSN, in the root field's argument.
deny_field[i] contains "PID_NOT_PRESENT" if {
	some i, f in lib.fields
	lib.key(f) == "Query.ingeschrevenPersoon"
	not helper.names_a_citizen(f, "bsn")
}

# Every requested year must be allowed.
deny_field[i] contains "YEAR_NOT_ALLOWED" if {
	some i, f in lib.fields
	lib.key(f) == "IngeschrevenPersoon.heeftBelastingjaarAangifte"
	some year in lib.requested_years(f, "belastingjaren")
	not year in allowed_years
}

# No year filter fails too: the source would return every year.
deny_field[i] contains "YEAR_NOT_ALLOWED" if {
	some i, f in lib.fields
	lib.key(f) == "IngeschrevenPersoon.heeftBelastingjaarAangifte"
	count(lib.requested_years(f, "belastingjaren")) == 0
}
