package dvtp.gbo.rules.eud0002

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
# Not checked here: the PID signature and the wallet certificate.

rule_id := "EUD0002"

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

# Same issuers as EUD0001: the list says who may issue attestations at all,
# not which source they read.
allowed_actors := {
	"00000004000000004000",
	"0000009961MINEZK0000",
	"99999999900000000100",
}

# No scope or year check: the closed set of fields is what this rule grants.
spec := {
	"rule_id": "EUD0002",
	"consent_required": false,
	"consent_must_cover_scope": false,
	"pid_required": true,
	# The disclosed BSN, in the root field's argument.
	"subject_argument": {"field": "Query.akteVanOverlijden", "arg": "bsn"},
	"allowed_actors": allowed_actors,
	"years_in_scopes": false,
}
