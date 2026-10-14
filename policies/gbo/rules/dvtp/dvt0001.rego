package gbo.rules.dvtp.dvt0001

# DVT0001 — Income data under a citizen's consent.
#
# Grants a service provider the income declaration fields when the citizen's
# consent is valid, not withdrawn or expired, given to that provider, and
# covers the scope and every requested year. The query names its subject with
# the identity placeholder, so the subject can only be the consent's citizen.
#
# How to read this rule
#
# requirements lists what must hold for the request as a whole. Each entry
# is a condition ("holds") and the code a refused field gets when it does
# not hold.
# checks(f) lists what must hold for one field, chosen by the field's key.
# A field without conditions of its own gets an empty list.
# A field is allowed when every condition, for the request and for the
# field, is true. A condition that cannot be evaluated at all makes the
# whole list disappear, and then the field is refused: nothing passes
# unchecked.

import data.gbo.consent
import data.gbo.lib

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

# ── The request ──────────────────────────────────────────────────────────────

requirements := [
	# The consent is valid: verified, known to the register, not withdrawn,
	# not expired. The code says which of these failed.
	{"holds": consent.valid, "code": consent.problem},
	# It was given to the party the call is made for.
	{"holds": consent.given_to_caller, "code": "CONSENT_ACTOR_MISMATCH"},
	# It covers the scope the consumer declares.
	{"holds": consent.covers_declared_scope, "code": "CONSENT_SCOPE_MISMATCH"},
	# That scope is the income question, for one belastingjaar. The consent
	# checks alone would not do: a consent for lvg:vbo:eigendom, sent with
	# that scope, covers it.
	{"holds": regex.match(`^bd:ib:[0-9]{4}$`, lib.declared_scope), "code": "SCOPE_NOT_ALLOWED"},
]

# ── The fields ───────────────────────────────────────────────────────────────

field_requirements[i] := checks(f) if some i, f in lib.fields

# The question is about the consent's citizen: bsn is the identity
# placeholder, for which the source puts in the BSN from the verified
# consent token, not a BSN the provider chose. The income API takes a BSN,
# not a pseudonym.
checks(f) := [{"holds": lib.argument_is(f, "bsn", "consent:identity"), "code": "CONSTRAINT_MISMATCH"}] if {
	lib.key(f) == "Query.ingeschrevenPersoon"
}

# Years were asked for, and the consent covers each of them: a consent for
# 2025 only refuses a query for 2024 and 2025. No year filter refuses too,
# as the source would return every year.
checks(f) := [{"holds": years_consented(f), "code": "YEAR_NOT_COVERED"}] if {
	lib.key(f) == "IngeschrevenPersoon.heeftBelastingjaarAangifte"
}

# Every other field has no condition of its own.
checks(f) := [] if not lib.key(f) in {"Query.ingeschrevenPersoon", "IngeschrevenPersoon.heeftBelastingjaarAangifte"}

default years_consented(_) := false

years_consented(f) if {
	years := lib.argument_values(f, "belastingjaren")
	count(years) > 0
	every year in years {
		consent.covers(sprintf("bd:ib:%d", [year]))
	}
}
