package gbo.lib

# ═══════════════════════════════════════════════════════════════════════════
# Reading the request, for every rule in either regime: the mapper's fields
# and their arguments, the caller, and the time. No
# checks live here; the rules under ./rules hold those.
# ═══════════════════════════════════════════════════════════════════════════

# ── Fields ───────────────────────────────────────────────────────────────────
# Every record the mapper produced for the request (FTV profile Section 6.2),
# __typename and introspection included. A rule that judges one field writes
# deny_field[i], with i the record's position in this list.

fields := object.get(input, ["resource", "attributes", "graphql", "fields"], [])

# A field's key, ParentType.field: what covers_fields names.
key(f) := sprintf("%s.%s", [f.parentType, f.field])

# The value of an argument as the request supplied it. Undefined when absent
# or (partly) from a schema default: the source applies its own default,
# which need not match the bundled schema, so a check on it must fail
# (Section 9.4).
#
# Do not negate a comparison on it: `not argument(f, a) == x` and
# `argument(f, a) != x` both let a missing value through, because Rego
# evaluates the call before the negation, and an undefined result fails the
# whole rule instead. Ask through a rule or function of its own, negated as
# a whole (`not helper.names_consent_citizen(f, ...)`), so that `not`
# negates the whole question.
argument(f, name) := entry.value if {
	entry := f.args[name]
	entry.origin != "schema-default"
	not entry.schemaDefaults
}

# The values a list argument was given, as a set (the mapper coerces a single
# value to a list). Empty when the request gave none: the argument is
# missing, null, or from a schema default. A filter check then refuses on
# count(...) == 0, so no filter never passes: without one the source
# returns everything.
argument_values(f, name) := {v | some v in argument(f, name)}

# ── Placeholders ─────────────────────────────────────────────────────────────
# The names a consent-based query gives its subject. The consumer holds no
# identifier of the citizen; after an allow the source puts in their place
# its own value from the consent token: the BSN for identity, its pseudonym
# of the citizen for pseudonym.

identity_placeholder := "consent:identity"

pseudonym_placeholder := "consent:pseudonym"

subject_placeholders := {identity_placeholder, pseudonym_placeholder}

# ── Caller (FSC delegation) ──────────────────────────────────────────────────
# A service provider calls a source itself, or through an integrator under a
# DelegatedServiceConnection grant. The Inway then gives the integrator as
# subject.id and the provider as outway_delegator_peer_id. Both come from a
# token the source's own Manager signed, on a contract all three parties
# signed, so the delegator is as trustworthy as subject.id.

acting_party := object.get(input, ["subject", "id"], "")

represented_party := delegator if {
	delegator := object.get(input, ["subject", "attributes", "outway_delegator_peer_id"], "")
	is_string(delegator)
	delegator != ""
} else := acting_party

default delegated := false

delegated if represented_party != acting_party

# ── Time ─────────────────────────────────────────────────────────────────────

now_ns := time.parse_rfc3339_ns(t) if {
	t := object.get(input, ["context", "time"], "")
	is_string(t)
	t != ""
} else := time.now_ns()
