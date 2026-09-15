package dvtp.gbo.rules.eud0001

# EUD0001 — Income declaration via EUDI-wallet attestation.
#
# Second policy-path alongside DVT0001. Same fields and use-case domain
# (income data for mortgage/IB), different authorization basis: instead
# of per-request citizen consent (DvTP-flow), the wallet-app receives
# the credential after PID-disclosure (EUDI-flow).
#
# Selection: this rule fires on the evidence the request carries, not on a
# declared flow. pid_required makes it fail closed unless the request is in
# the PID regime — no consent token, and a subject named — exactly as
# DVT0001's consent_required does without a verified consent. The two
# regimes exclude each other by construction, so on any request at most one
# of the two rules applies, although both cover the same fields.
#
# Deliberately NOT in this V1 spec:
#   - PID-signature verification (adapter trusts BSN from disclosed PID)
#   - Wallet-cert check
#   - Attestation-type whitelist
# These belong to the EUDI-adapter or to a richer spec later.

rule_id := "EUD0001"

# Same covers as DVT0001, plus box 2 and box 3 — both flows may see the
# income declaration in full. Divergence (e.g. EUDI sees fewer fields)
# comes later.
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

# The policy authorizes the concrete selector in the source-owned query.
# There is no GBO usecase-catalog or derived bd:ib:<year> scope in this path.
allowed_years := {2024, 2025}

# Per-rule actor-whitelist: which subject.id (OIN) may trigger this rule?
# Designated EDI-issuer along the lines of eIDAS art. 5a-style
# designation. Extra gate above the FSC-transport check: FSC verifies
# that the OIN has a bri-grant; this axis verifies that the OIN is also
# allowed at rule-level for income-declaration issuance.
allowed_actors := {
	"00000004000000004000",
	"0000009961MINEZK0000",
	"99999999900000000100",
}

# Evaluation spec: PID present + requested year in allowed_years + actor in
# allowed_actors. All checks apply to the actual query sent to the source.
spec := {
	"rule_id": "EUD0001",
	"consent_required": false,
	"consent_must_cover_scope": false,
	"pid_required": true,
	"allowed_years": allowed_years,
	"allowed_actors": allowed_actors,
	"years_in_scopes": false,
	"pip": null,
}
