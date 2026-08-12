package dvtp.gbo.rules.eud0002

# EUD0002 — Akte van overlijden via EUDI-wallet attestation.
#
# Third policy-path, over the source-owned BRP attestation view. The
# nabestaande discloses her PID; the source resolves the legally relevant
# huwelijk and exposes only the fields that can enter the credential.
#
# Dispatch: like EUD0001 this rule fires only for eudi:attestation.
# The flow selects the PID-based authorization model, not the source.
# The two EUDI rules never compete for a field because their
# covers_fields are disjoint.
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

covers_fields := {"Query.akteVanOverlijden"} | {
sprintf("AkteVanOverlijden.%s", [field]) |
	some field in {
		"overledene_geslachtsnaam", "overledene_voorvoegsel", "overledene_voornamen",
		"overledene_geboortedatum", "overledene_geboorteplaats", "overledene_geboorteland",
		"overledene_geslacht", "overledene_ouders", "datum_overlijden", "plaats_overlijden",
		"land_overlijden", "soort_verbintenis", "echtgenoot_geslachtsnaam",
		"echtgenoot_voorvoegsel", "echtgenoot_voornamen", "verklaring_tekst",
	}
}

# Same designated EDI-issuers as EUD0001: the actor-whitelist is about who
# may have an attestation issued at all, not about which bron it reads.
allowed_actors := {
	"00000004000000004000",
	"0000009961MINEZK0000",
	"99999999900000000100",
}

# Evaluation spec: PID present + designated actor. The closed-world field set
# is the authorization surface; no catalog scope is manufactured by GBO.
spec := {
	"rule_id": "EUD0002",
	"flow": "eudi:attestation",
	"consent_required": false,
	"consent_must_cover_scope": false,
	"pid_required": true,
	"allowed_actors": allowed_actors,
	"years_in_scopes": false,
	"pip": null,
}
