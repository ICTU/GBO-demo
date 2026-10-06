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
# names its subject with the identity placeholder, so that the subject can
# only be the one that signed consent carries.

rule_id := "DVT0001"

# Object-types whose SCALAR fields we cover (inheritance):
# Bedrag (the amount object under verzamelinkomen/box*Inkomen) — all its
# scalars (waarde, valuta) inherit coverage from this rule.
# BelastingjaarAangifte/AangifteIH are NOT listed here: we declare their
# covered fields explicitly in covers_fields, so that a field we
# deliberately do NOT cover (e.g. box2Inkomen/box3Inkomen) is denied —
# model C: the rule IS the catalog, no separate scope_fields table. The
# reason is NO_APPLICABLE_RULE only when no other rule covers the field
# either; box2Inkomen/box3Inkomen are covered by EUD0001, so a consent
# request for them is denied with its PID_NOT_PRESENT.
covers_types := {"Bedrag"}

# Explicitly covered fields: ALL object-edges + the scalars this rule
# grants. Anything not listed here falls outside coverage → the engine's
# closed-world default gives NO_APPLICABLE_RULE → DENY.
#
# Fields that live on the BelastingjaarAangifte interface are declared for
# both the interface and the concrete AangifteIH: the resolved parent-type
# depends on whether the query selects the field at interface level or
# inside an `... on AangifteIH` fragment.
covers_fields := {
	# object-edges (parent-traversal requires these)
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

# Evaluation spec: which checks must hold for access. lib.evaluate runs
# this cascade and returns the first failing DENY-reason.
spec := {
	"rule_id": "DVT0001",
	"consent_required": true,
	"consent_context_required": true,
	"consent_status_required": true,
	"consent_actor_binding": true,
	"consent_must_cover_scope": true,
	# Field-coverage now comes from covers_fields above (model C). A
	# field we do not explicitly include → the engine's closed-world
	# default denies with NO_APPLICABLE_RULE. No separate field-axis
	# in lib anymore.
	# The query names its subject with a placeholder in the bsn-arg: the
	# service provider holds no identifier of the citizen. The subject is
	# the one the verified consent token carries, and the source puts its
	# own decrypted value in place after this allow. The income API takes a
	# BSN, so only the identity placeholder is accepted. A literal value
	# here would be a subject the caller chose, so it is denied.
	"constraint_binding": [{
		"arg": "bsn",
		"placeholders": {"consent:identity"},
	}],
	# Per-year consent: every belastingjaar in the query's belastingjaren
	# filter must be covered by a bd:ib:<year> scope in the consent. A
	# citizen who consents to 2025 but not 2024 makes a query for both
	# years fail with YEAR_NOT_COVERED.
	"years_in_scopes": true,
	"pip": null,
}
