package dvtp.gbo.rules.eud0001

# EUD0001 — Income declaration for an EUDI wallet attestation.
#
# Grants a designated issuer the income declaration in the PID regime: no
# consent token, and the BSN the citizen disclosed from the wallet's PID.
# pid_required fails closed outside that regime, so on any request at most
# one of DVT0001 and EUD0001 applies, although they share fields.
#
# Not checked here: the PID signature (the adapter trusts the disclosure),
# the wallet certificate and the attestation type.

rule_id := "EUD0001"

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
allowed_years := {2024, 2025}

# The designated issuers (as in eIDAS art. 5a designation). The FSC grant
# admits a peer to the service; this admits it to this rule.
allowed_actors := {
	"00000004000000004000",
	"0000009961MINEZK0000",
	"99999999900000000100",
}

spec := {
	"rule_id": "EUD0001",
	"consent_required": false,
	"consent_must_cover_scope": false,
	"pid_required": true,
	# The disclosed BSN, in the root field's argument.
	"subject_argument": {"field": "Query.ingeschrevenPersoon", "arg": "bsn"},
	"allowed_years": allowed_years,
	"years_argument": {"field": "IngeschrevenPersoon.heeftBelastingjaarAangifte", "arg": "belastingjaren"},
	"allowed_actors": allowed_actors,
	"years_in_scopes": false,
}
