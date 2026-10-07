package dvtp.gbo.rules.dvt0001

# DVT0001 — Income data under a citizen's consent.
#
# Grants a service provider the income declaration fields when the citizen's
# consent is valid, not withdrawn or expired, given to that provider, and
# covers the scope and every requested year. The query names its subject with
# the identity placeholder, so the subject can only be the consent's citizen.

rule_id := "DVT0001"

# Bedrag's scalars (waarde, valuta) are covered through their type. The
# declaration types are listed field by field instead, so box2Inkomen and
# box3Inkomen stay uncovered here; under consent EUD0001 denies them with
# PID_NOT_PRESENT.
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

spec := {
	"rule_id": "DVT0001",
	"consent_required": true,
	"consent_context_required": true,
	"consent_status_required": true,
	"consent_actor_binding": true,
	"consent_must_cover_scope": true,
	# The root field's bsn must be the identity placeholder: the provider
	# holds no identifier of the citizen, and the source puts in the BSN from
	# the verified consent token. A literal BSN would be a subject the caller
	# chose, so it is denied. The income API takes a BSN, not a pseudonym.
	"constraint_binding": [{
		"field": "Query.ingeschrevenPersoon",
		"arg": "bsn",
		"placeholders": {"consent:identity"},
	}],
	# Every requested belastingjaar needs a bd:ib:<year> scope in the
	# consent: a consent for 2025 only denies a query for 2024 and 2025.
	"years_in_scopes": true,
	"years_argument": {"field": "IngeschrevenPersoon.heeftBelastingjaarAangifte", "arg": "belastingjaren"},
}
