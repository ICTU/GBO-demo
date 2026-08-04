package dvtp.gbo.rules.eud0002

# EUD0002 — Akte van overlijden via EUDI-wallet attestation.
#
# Third policy-path, and the first one over the BRP bron (bronprofiel
# Basisregistratie Personen) instead of the BD bron. The nabestaande
# discloses her PID; the query walks from her own persoonslijst to
# heeftHuwelijk -> partners, where the overleden partner sits.
#
# Dispatch: this rule fires only for the BRP-specific
# eudi:attestation:brp flow. The two EUDI rules never compete for a field —
# their covers_fields are disjoint because they cover different bronprofielen.
#
# Note on the data subject: the query is rooted at the requester's own BSN
# (the disclosed PID), and everything else is reached through her own
# persoonslijst. That is the authorization argument for releasing another
# person's overlijdensgegevens here: only the huwelijkspartner can walk
# this path, and only for the marriage she is herself a party to.
#
# Deliberately NOT in this V1 spec (same carve-outs as EUD0001):
#   - PID-signature verification (adapter trusts BSN from disclosed PID)
#   - Wallet-cert check
#   - A check that the verbintenis is actually ontbonden by overlijden —
#     that is a data-shape concern, enforced by the adapter (no overleden
#     partner -> no attestation), not a policy axis.

rule_id := "EUD0002"

# No covers_types: unlike the BD path (Bedrag) there is no shared value-
# object here whose scalars need type-level inheritance. Every field the
# akte-query touches is declared explicitly below, so the engine's
# closed-world default (NO_APPLICABLE_RULE) catches anything broader —
# e.g. woontOp, heeftNationaliteit or gezag are BRP fields this rule
# deliberately does NOT cover.
covers_types := set()

covers_fields := {
	"Query.ingeschrevenPersoon",
	# The nabestaande herself — identity of the aanvrager on the akte.
	"IngeschrevenPersoon.id",
	"IngeschrevenPersoon.bsn",
	"IngeschrevenPersoon.geslachtsnaam",
	"IngeschrevenPersoon.voorvoegsel",
	"IngeschrevenPersoon.voornamen",
	"IngeschrevenPersoon.heeftHuwelijk",
	# De verbintenis: wanneer, waar, en hoe ontbonden.
	"Huwelijk.soortVerbintenis",
	"Huwelijk.datumVoltrekking",
	"Huwelijk.plaatsVoltrekking",
	"Huwelijk.landVoltrekking",
	"Huwelijk.datumOntbinding",
	"Huwelijk.redenOntbinding",
	"Huwelijk.partners",
	# De partners van die verbintenis — inclusief de overledene. `partners`
	# is symmetrisch, dus dit zijn dezelfde velden voor beide echtgenoten.
	"NatuurlijkPersoon.id",
	"NatuurlijkPersoon.geslachtsnaam",
	"NatuurlijkPersoon.voorvoegsel",
	"NatuurlijkPersoon.voornamen",
	"NatuurlijkPersoon.geboortedatum",
	"NatuurlijkPersoon.geboorteplaats",
	"NatuurlijkPersoon.geboorteland",
	"NatuurlijkPersoon.geslacht",
	"NatuurlijkPersoon.datumOverlijden",
	"NatuurlijkPersoon.plaatsOverlijden",
	"NatuurlijkPersoon.landOverlijden",
	# De ouders van de overledene staan op een akte van overlijden.
	"NatuurlijkPersoon.heeftOuder",
}

# Per-rule scope-whitelist. One scope for one akte — the BD path's
# per-year axis has no counterpart here, so the scope is the whole
# authorization surface next to the actor-whitelist.
allowed_scopes := {"brp:akte:overlijden"}

# Same designated EDI-issuers as EUD0001: the actor-whitelist is about who
# may have an attestation issued at all, not about which bron it reads.
allowed_actors := {
	"00000004000000004000",
	"0000009961EUDIISS000",
	"99999999900000000100",
}

# Evaluation spec: PID present + BSN 9 digits + scope in allowed_scopes +
# actor in allowed_actors. years_in_scopes is off — the BRP query has no
# year selector, and leaving the axis on would deny every request with
# YEAR_NOT_COVERED.
spec := {
	"rule_id": "EUD0002",
	"flow": "eudi:attestation:brp",
	"consent_required": false,
	"consent_must_cover_scope": false,
	"pid_required": true,
	"allowed_scopes": allowed_scopes,
	"allowed_actors": allowed_actors,
	"years_in_scopes": false,
	"pip": null,
}
