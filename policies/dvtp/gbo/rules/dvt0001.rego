package dvtp.gbo.rules.dvt0001

# DVT0001 — Income data via consent.
#
# Self-contained rule for the GBO rule-engine: the rule itself carries its
# scope (covers_types + covers_fields) and its evaluation criteria (spec).
# The engine binds this rule to every requested field that falls within
# scope and evaluates the spec via dvtp.gbo.lib.evaluate.
#
# Semantics: this rule grants a service provider (= consumer) access to
# income-data fields IF a valid citizen consent exists for (consumer,
# scope, fields), the consent is not withdrawn or expired, and the query
# carries a correct consent_id-binding.

rule_id := "DVT0001"

# Object-types whose SCALAR fields we cover (inheritance):
# CodeOmschrijving (the sub-object under grondslag/status) — all its
# scalars (code, omschrijving) inherit coverage from this rule.
# InkomensgegevensPerJaar is NOT listed here: we declare its covered
# scalars explicitly in covers_fields, so that a field we deliberately
# do NOT cover (e.g. inkomenUitBox2/3) comes back as NO_APPLICABLE_RULE
# — model C: the rule IS the catalog, no separate scope_fields table.
covers_types := {"CodeOmschrijving"}

# Explicitly covered fields: ALL object-edges + the scalars this rule
# grants. Anything not listed here falls outside coverage → the engine's
# closed-world default gives NO_APPLICABLE_RULE → DENY.
covers_fields := {
	# object-edges (parent-traversal requires these)
	"Query.inkomensgegevens",
	"InkomensgegevensPerJaar.grondslag",
	"InkomensgegevensPerJaar.status",
	# scalar fields for the mortgage/IB use-case
	"InkomensgegevensPerJaar.belastingjaar",
	"InkomensgegevensPerJaar.verzamelinkomen",
	"InkomensgegevensPerJaar.inkomenUitBox1",
	"InkomensgegevensPerJaar.peilDatum",
}

# Evaluation spec: which checks must hold for access. lib.evaluate runs
# this cascade and returns the first failing DENY-reason.
spec := {
	"rule_id": "DVT0001",
	"consent_required": true,
	"consent_must_cover_scope": true,
	# Field-coverage now comes from covers_fields above (model C). A
	# field we do not explicitly include → the engine's closed-world
	# default denies with NO_APPLICABLE_RULE. No separate field-axis
	# in lib anymore.
	# The query supplies PI in the bsn-arg (the service provider holds
	# PI via BSNk); consent-PIP-lookup by-PI+scope fills pip.consent.pi.
	# Binding: the pseudonym in the query must match the pseudonym in
	# the fetched consent — proving that this query is executed for this
	# consent, not a different consent from the same consumer.
	"constraint_binding": [{
		"arg": "input.burgerservicenummer",
		"resource_field": "pi",
	}],
	"pip": null,
}
