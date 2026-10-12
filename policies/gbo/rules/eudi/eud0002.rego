package gbo.rules.eudi.eud0002

# EUD0002 — Akte van overlijden for an EUDI wallet attestation.
#
# Grants a designated issuer the death certificate view of the BRP, in the
# PID regime only. The surviving spouse discloses her PID; the source
# resolves the relevant marriage and exposes only the fields that can enter
# the credential. Its fields are disjoint from EUD0001's.
#
# The PDP sees the resolver and its flat output, not how the source picks the
# marriage from her own persoonslijst. Picking the partner whose death ended
# it is a security-sensitive source invariant, tested in the BRP service.
#
# Not checked here: the PID signature and the wallet certificate. No scope
# or year check: the closed set of fields is what this rule grants.

import data.gbo.lib
import data.gbo.rules.eudi.helper

rule_id := "EUD0002"

regime := "pid"

# No shared value object here. Every field is listed, so other BRP fields
# (woontOp, heeftNationaliteit, gezag) fall to the engine's closed world.
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

# ── The caller ───────────────────────────────────────────────────────────────

deny contains "PID_NOT_PRESENT" if helper.consent_token_presented

deny contains "ACTOR_NOT_ALLOWED" if not helper.issuer_designated

# ── The question ─────────────────────────────────────────────────────────────

# The disclosed BSN, in the root field's argument.
deny_field[i] contains "PID_NOT_PRESENT" if {
	some i, f in lib.fields
	lib.key(f) == "Query.akteVanOverlijden"
	not helper.names_a_citizen(f, "bsn")
}
