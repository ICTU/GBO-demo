package gbo.lib

# ═══════════════════════════════════════════════════════════════════════════
# Reading the request, for every rule in either regime: the mapper's fields
# and their arguments, the declared scope, the caller, and the time. No
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
# (Section 9.4). Write such a check as `not argument(f, a) == x`, never
# `argument(f, a) != x`: the latter is undefined, not true, for a missing
# value and would let the field through.
argument(f, name) := entry.value if {
	entry := f.args[name]
	entry.origin != "schema-default"
	not entry.schemaDefaults
}

# The years an argument asks for, as text: the mapper coerces a single value
# to a list, and a year may arrive as number or string. Empty when the
# argument is missing, so a year check fails closed: the source returns
# every year unless the query filters.
requested_years(f, name) := {sprintf("%v", [y]) | some y in argument(f, name)}

# ── Placeholders ─────────────────────────────────────────────────────────────
# The names a consent-based query gives its subject. The consumer holds no
# identifier of the citizen; after an allow the source puts in their place
# its own value from the consent token: the BSN for identity, its pseudonym
# of the citizen for pseudonym.

identity_placeholder := "consent:identity"

pseudonym_placeholder := "consent:pseudonym"

subject_placeholders := {identity_placeholder, pseudonym_placeholder}

# ── Scope ────────────────────────────────────────────────────────────────────
# The scope the consumer declares in X-GBO-Scope, header name in any case.
# Untrusted: the rules check it against the consent's scopes or their own.
# Two values give no scope rather than one picked: the pick would be
# arbitrary, and the source could act on the other.

_scope_values contains value if {
	some name, value in object.get(input, ["context", "headers"], {})
	lower(name) == "x-gbo-scope"
	is_string(value)
}

declared_scope := scope if {
	count(_scope_values) == 1
	some scope in _scope_values
} else := ""

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
