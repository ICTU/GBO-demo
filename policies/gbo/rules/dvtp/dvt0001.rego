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
# The file has three parts. The rule: requirements lists what must hold for
# the request as a whole, and requirements_for(f) what must hold for one
# field, chosen by the field's key. Each line is require(condition, code):
# the condition, and the code a refused field gets when it does not hold.
# The conditions: each defined with a default of false and one body that
# says when it holds. From requirements to deny: the same in every rule, it
# turns each condition that does not hold into a refusal with its code.
#
# A field is allowed when every condition, for the request and for the
# field, is true. A condition that cannot be evaluated at all makes its
# whole list disappear, and then the field is refused: nothing passes
# unchecked.

import data.gbo.consent
import data.gbo.lib
import data.gbo.lib.require

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

# ── The rule ─────────────────────────────────────────────────────────────────

requirements := [
	require(consent.valid, consent.problem),
	require(consent.given_to_caller, "CONSENT_ACTOR_MISMATCH"),
	require(consent.covers_declared_scope, "CONSENT_SCOPE_MISMATCH"),
	require(asks_the_income_question, "SCOPE_NOT_ALLOWED"),
]

# For every field of the request, its list is requirements_for(f). This line
# is the same in every rule: OPA cannot let the engine call a function for
# each field, so the rule lays the lists out by the field's position itself.
field_requirements[i] := requirements_for(f) if some i, f in lib.fields

requirements_for(f) := [require(asks_about_the_consenting_citizen(f), "CONSTRAINT_MISMATCH")] if {
	lib.key(f) == "Query.ingeschrevenPersoon"
}

requirements_for(f) := [require(years_are_consented(f), "YEAR_NOT_COVERED")] if {
	lib.key(f) == "IngeschrevenPersoon.heeftBelastingjaarAangifte"
}

requirements_for(f) := [] if not lib.key(f) in fields_with_conditions

# The fields that have a condition of their own, above. Every other field
# has none and gets an empty list.
fields_with_conditions := {"Query.ingeschrevenPersoon", "IngeschrevenPersoon.heeftBelastingjaarAangifte"}

# ── The conditions ───────────────────────────────────────────────────────────

# The consumer asks the income question, for one belastingjaar. The consent
# checks alone would not do: a consent for lvg:vbo:eigendom, sent with that
# scope, covers it.
default asks_the_income_question := false

asks_the_income_question if regex.match(`^bd:ib:[0-9]{4}$`, lib.declared_scope)

# The question is about the citizen who consented: bsn is the identity
# placeholder, which the source replaces with the BSN from the verified
# consent token, not a BSN the provider chose. The income API takes a BSN,
# not a pseudonym.
default asks_about_the_consenting_citizen(_) := false

asks_about_the_consenting_citizen(f) if lib.argument_is(f, "bsn", "consent:identity")

# Years were asked for, and the consent covers each of them: a consent for
# 2025 only refuses a query for 2024 and 2025. No year filter fails too, as
# the source would return every year.
default years_are_consented(_) := false

years_are_consented(f) if {
	years := lib.argument_values(f, "belastingjaren")
	count(years) > 0
	every year in years {
		consent.covers(sprintf("bd:ib:%d", [year]))
	}
}

# ── From requirements to deny ────────────────────────────────────────────────
# The same in every rule written this way; the engine reads only deny and
# deny_field. A field is refused with the code of each requirement that does
# not hold. A list that is missing could not be evaluated, and refuses too.
# A field with arguments needs a condition of its own: without one, any
# value of its arguments would pass.

deny contains r.code if {
	some r in requirements
	not holds(r)
}

deny contains "RULE_NOT_EVALUABLE" if not requirements

deny_field[i] contains r.code if {
	some i, list in field_requirements
	some r in list
	not holds(r)
}

deny_field[i] contains "RULE_NOT_EVALUABLE" if {
	some i, _ in lib.fields
	not field_requirements[i]
}

deny_field[i] contains "FIELD_ARGUMENTS_UNCHECKED" if {
	some i, f in lib.fields
	count(object.get(f, "args", {})) > 0
	field_requirements[i] == []
}

# Only a condition that is exactly true holds. Asked through a rule of its
# own, so that `not` negates the whole question.
holds(r) if r.holds == true
