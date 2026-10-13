package gbo

import data.gbo.lib

# ═══════════════════════════════════════════════════════════════════════════
# GBO PDP runtime of the FTV GraphQL profile's policy model (Section 9). It
# binds each data field of the mapper's field list to the rules in ./rules,
# collects their reasons to refuse it, and allows the request only when every
# data field is allowed.
# ═══════════════════════════════════════════════════════════════════════════

import data.gbo.consent

# ── Entrypoint: allowed only when every data field is allowed ──────────────

_field_decisions := [{"field": f.id, "path": f.record.path, "key": f.key, "index": f.index, "result": _decision_on(f)} | some f in _data_fields]

# A field is never dropped: one whose decision cannot be made, such as under
# a rule that does not evaluate, is refused as a fault in the policy.
# Dropped, it would leave every other field to decide alone.
_decision_on(f) := result if {
	result := _decide(f)
} else := {"decision": false, "context": {"reason_admin": {"code": "RULE_NOT_EVALUABLE", "evaluated": []}}}

# The resolved consent is added to the response: the policy fetched it, so
# without this the decision log would lack the attribute it decided on.
response := object.union(_decision, {"context": {"pip": {"consent": consent.resolved}}}) if {
	consent.resolved
} else := _decision

# OpenFTV passes on only the decision and authz.rego's `reason`. A deny
# carries the profile's context (Section 10.1): the same text as reason_user
# and reason_admin, graphql.client with what the consumer may learn, and
# graphql.admin with the detail. granted, the denied fields and their trace
# are also what the developer portal (a demo feature) shows.
_decision := {"decision": true, "context": {"granted": _granted}} if {
	not _request_failure
	count(_field_decisions) > 0
	every fd in _field_decisions {
		fd.result.decision == true
	}
} else := {"decision": false, "context": {
	"reason_admin": {"en": _reason_text},
	"reason_user": {"en": _reason_text},
	"graphql": {"client": _client, "admin": _admin},
}}

_granted := [{
	"field": fd.field,
	"key": fd.key,
	"rule": fd.result.context.granted_by,
} |
	some fd in _field_decisions
]

# Per denied field: its position, path and key, its code, the rules that
# were bound, and each rule's code and reasons.
_denied_fields := [{
	"index": fd.index,
	"path": fd.path,
	"key": fd.key,
	"code": fd.result.context.reason_admin.code,
	"evaluated": [e.rule | some e in fd.result.context.reason_admin.evaluated],
	"trace": fd.result.context.reason_admin.evaluated,
} |
	some fd in _field_decisions
	fd.result.decision == false
]

# ── The request's code (Section 9.5) and what the consumer sees (10.2) ──────

# A check on the mapper's output fails first; then a field no rule could
# decide; then a server problem on any field; else the request is denied
# because fields are.
_request_code := _request_failure if {
	_request_failure
} else := {"code": "CONFIG_ERROR", "subcode": "RULE_NOT_EVALUABLE"} if {
	some d in _denied_fields
	d.code == "RULE_NOT_EVALUABLE"
} else := {"code": "PIP_UNAVAILABLE"} if {
	some d in _denied_fields
	d.code == "PIP_UNAVAILABLE"
} else := {"code": "FIELD_NOT_PERMITTED"}

_admin := object.union(object.union(_request_code, _admin_digest), _admin_fields)

_admin_digest := {"schema_digest": _gql.schema.digest} if {
	is_string(_gql.schema.digest)
} else := {}

_admin_fields := {"denied_fields": _denied_fields} if {
	not _request_failure
} else := {}

# Server problems are ours, not the consumer's: it learns only that access
# was denied. The consumer is also not told which field was refused, nor
# whether an unknown field exists (Section 14): both would let it map the
# schema by trying names.
_server_class := {"CONFIG_ERROR", "PIP_UNAVAILABLE"}

_client := {"code": "ACCESS_DENIED"} if {
	_request_code.code in _server_class
} else := {"code": "FIELD_NOT_PERMITTED"} if {
	_request_code.subcode == "INVALID_QUERY"
} else := object.filter(_request_code, {"code", "subcode"})

_client_texts := {
	"FIELD_NOT_PERMITTED": "Field not permitted",
	"ACCESS_DENIED": "Access denied",
	"COVERAGE_UNVERIFIABLE": "Request could not be verified",
	"OPERATION_NOT_SUPPORTED": "Operation not supported",
	"NO_DATA_FIELDS": "No data fields requested",
}

# reason_admin gets the same hiding as reason_user: some PEPs pass it on.
_reason_text := sprintf("%s (%s)", [object.get(_client_texts, _client.code, _client.code), _client.subcode]) if {
	_client.subcode
} else := object.get(_client_texts, _client.code, _client.code)

# ── Checks on the mapper's output, before any rule (Section 9.5) ────────────
# Checked in this order; any failure denies the whole request.

_profile := "ftv-graphql/0.1"

_gql := input.resource.attributes.graphql

_service := object.get(object.get(input.subject, "attributes", {}), "service_name", "")

_request_failure := {"code": "CONFIG_ERROR", "subcode": "MAPPER_OUTPUT_MISSING"} if {
	not _gql.profile == _profile
} else := {"code": "CONFIG_ERROR", "subcode": "SCHEMA_MISMATCH"} if {
	_gql.schema != null
	not _schema_pinned
} else := object.filter(_gql.unverifiable, {"code", "subcode", "message"}) if {
	_gql.unverifiable != null
} else := {"code": "NO_DATA_FIELDS"} if {
	count(_data_fields) == 0
}

# The bundle pins each service's schema digest; a service without one fails.
_schema_pinned if _gql.schema.digest == data.gbo.graphql_schemas.digests[_service]

# ── The rules ───────────────────────────────────────────────────────────────
# Every package in a directory under ./rules (rules/asi, rules/dvtp,
# rules/eudi) that names a rule_id is a rule. deny holds its reasons about the
# request as a whole, deny_field[i] those about data field i; a rule grants a
# field when neither has a reason for it.

_rule_meta[rid] := {
	"regime": _rule_doc(dir, pkg, "regime", ""),
	"covers_types": _rule_doc(dir, pkg, "covers_types", set()),
	"covers_fields": _rule_doc(dir, pkg, "covers_fields", set()),
	"services": _rule_doc(dir, pkg, "services", null),
	"deny": _rule_doc(dir, pkg, "deny", set()),
	"deny_field": _rule_doc(dir, pkg, "deny_field", {}),
} if {
	some dir, pkg
	rid := data.gbo.rules[dir][pkg].rule_id
	is_string(rid)
}

# One document of a rule package, never the whole package: the rule's tests
# live next to it and would be evaluated with every decision.
_rule_doc(dir, pkg, name, _) := data.gbo.rules[dir][pkg][name]

_rule_doc(dir, pkg, name, fallback) := fallback if not data.gbo.rules[dir][pkg][name]

# ── The regime decides which rules apply ────────────────────────────────────
# A consent token, verified or not, puts the request in the consent regime
# (consent.resolved is set exactly then); without one, the PID regime. Only
# that regime's rules bind, plus rules naming no regime (test rules only).

_regime := "consent" if {
	consent.resolved
} else := "pid"

_active_rules[rid] := m if {
	some rid, m in _rule_meta
	m.regime in {"", _regime}
	_serves(m)
}

# ── The service decides which rules apply ───────────────────────────────────
# A field key names a type and field, not the schema: two services can share
# a key (Query.ingeschrevenPersoon is in both bri and brp). A rule naming
# services binds only on those, by the FSC service signed in the contract; a
# rule naming none binds on every service. An empty set binds nowhere.

_serves(m) if m.services == null

_serves(m) if _service in m.services

_field_declared contains key if {
	some _, m in _active_rules
	some key in m.covers_fields
}

_field_rules(key) := [rid |
	some rid, m in _active_rules
	key in m.covers_fields
]

_type_rules(t) := [rid |
	some rid, m in _active_rules
	t in m.covers_types
]

# The rules for a data field, by its key ParentType.field (Section 9.3): the
# rules naming the key; else, for a leaf without arguments outside the root
# types, the rules covering its parent type; else none (NO_APPLICABLE_RULE).
# Root fields, edges and fields with arguments bind only by their own key: a
# rule bound through a type does not check arguments, and would cover entry
# points added to the schema later.
default _effective_policy_ids(_, _) := []

_effective_policy_ids(_, key) := _field_rules(key) if _field_declared[key]

_effective_policy_ids(rf, key) := _type_rules(rf.parentType) if {
	not _field_declared[key]
	rf.leaf == true
	not rf.args
	not rf.parentType in _root_types
}

# ── Data fields (closed world) ──────────────────────────────────────────────
# Every record except __typename and introspection internals (Section 9.2),
# root fields included: they run a resolver. Each record is decided alone.

_root_types := {"Query", "Mutation", "Subscription"}

_data_fields := [df |
	some i, rf in _gql.fields
	rf.field != "__typename"
	not startswith(rf.parentType, "__")
	key := lib.key(rf)
	df := {
		"index": i,
		"id": concat(".", rf.path),
		"key": key,
		"record": rf,
		"policy_ids": _effective_policy_ids(rf, key),
	}
]

# ── The integrator's mandate ────────────────────────────────────────────────
# Every delegated call needs a mandate for the represented provider and the
# rule that grants, whatever the rule says: it is about who may act for
# whom, like the FSC delegation itself, so no rule can leave it out. The
# admission register is data.entities, pulled by OpenFTV; a direct call has
# no integrator to check.

_mandate_reasons(rid) := {"INTEGRATOR_REGISTER_UNAVAILABLE"} if {
	lib.delegated
	not data.entities.dvtp_participant
} else := {"INTEGRATOR_NOT_REGISTERED"} if {
	lib.delegated
	not _mandated(rid)
} else := set()

_mandated(rid) if {
	integrator := data.entities.dvtp_participant[lib.acting_party]
	integrator.active == true
	some mandate in object.get(integrator, "acts_for", [])
	mandate.peer_id == lib.represented_party
	rid in mandate.rules
}

# ── Per field: the first bound rule without reasons grants ─────────────────

# Each bound rule's reasons to refuse each data field: its own about the
# request and about this field, and the mandate. A partial rule is cached,
# a function call is not, and the per-field logic reads them repeatedly.
_reasons[i][rid] := reasons if {
	some f in _data_fields
	i := f.index
	some rid in f.policy_ids
	m := _rule_meta[rid]
	reasons := (m.deny | object.get(m.deny_field, i, set())) | _mandate_reasons(rid)
}

_decide(f) := {"decision": false, "context": {"reason_admin": {"code": "NO_APPLICABLE_RULE", "evaluated": []}}} if {
	count(f.policy_ids) == 0
}

_decide(f) := _evaluate_field(f.policy_ids, _reasons[f.index]) if {
	count(f.policy_ids) > 0
}

_evaluate_field(policy_ids, reasons) := {"decision": true, "context": {"granted_by": rid}} if {
	allowing := [r | some r in policy_ids; count(reasons[r]) == 0]
	count(allowing) > 0
	rid := allowing[0]
} else := result if {
	# No rule allowed. Each rule's reasons form the trace, its code the
	# first of them; the field's code follows the profile (Section 9.4): a
	# server problem if any rule's code is one, else the first rule's code.
	evaluated := [{"rule": r, "code": ordered[0], "reasons": ordered} |
		some r in policy_ids
		ordered := _ordered(reasons[r])
	]
	result := {
		"decision": false,
		"context": {"reason_admin": {"code": _field_code(evaluated), "evaluated": evaluated}},
	}
}

_field_code(evaluated) := "PIP_UNAVAILABLE" if {
	some e in evaluated
	e.code in _pip_unavailable
} else := evaluated[0].code

# The consent register, its keys or the admission register did not answer: a
# server problem, not the consumer's. The trace still shows which.
_pip_unavailable := {"CONSENT_STATUS_UNAVAILABLE", "CONSENT_KEYS_UNAVAILABLE", "INTEGRATOR_REGISTER_UNAVAILABLE"}

# A rule can give several reasons at once; its code is the first in this
# order, so an administrator sees the most fundamental one: the token, then
# the consent's state, then the question, then who asks. A reason not listed
# still refuses, and comes after the listed ones.
_reason_order := [
	"CONSENT_CONTEXT_INVALID",
	"CONSENT_SIGNATURE_INVALID",
	"CONSENT_KEYS_UNAVAILABLE",
	"CONSENT_TOKEN_EXPIRED",
	"CONSENT_STATUS_UNAVAILABLE",
	"CONSENT_NOT_FOUND",
	"CONSENT_WITHDRAWN",
	"CONSENT_EXPIRED",
	"CONSENT_SCOPE_MISMATCH",
	"CONSTRAINT_MISMATCH",
	"PID_NOT_PRESENT",
	"SCOPE_NOT_ALLOWED",
	"YEAR_NOT_ALLOWED",
	"YEAR_NOT_COVERED",
	"CONSENT_ACTOR_MISMATCH",
	"ACTOR_NOT_ALLOWED",
	"INTEGRATOR_REGISTER_UNAVAILABLE",
	"INTEGRATOR_NOT_REGISTERED",
]

_ordered(reasons) := array.concat(
	[r | some r in _reason_order; r in reasons],
	sort([r | some r in reasons; not r in _reason_order]),
)
