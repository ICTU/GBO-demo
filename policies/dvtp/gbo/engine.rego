package dvtp.gbo

import data.dvtp.gbo.lib

# ═══════════════════════════════════════════════════════════════════════════
# GBO PDP runtime of the FTV GraphQL profile's policy model (Section 9). It
# binds each data field of the mapper's field list to the rules in ./rules,
# evaluates them with lib.evaluate, and allows the request only when every
# data field is allowed.
# ═══════════════════════════════════════════════════════════════════════════

import data.dvtp.gbo.consent

# ── Entrypoint: allowed only when every data field is allowed ──────────────

_field_decisions := [{"field": f.id, "path": f.record.path, "key": f.key, "index": f.index, "result": _decide(f)} | some f in _data_fields]

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
	"steps": object.get(fd.result.context, "granted_steps", []),
} |
	some fd in _field_decisions
]

# Per denied field: its position, path and key, its code, the rules that
# were bound, and each rule's trace.
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

# A check on the mapper's output fails first; then a server problem on any
# field; else the request is denied because fields are.
_request_code := _request_failure if {
	_request_failure
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
_schema_pinned if _gql.schema.digest == data.dvtp.gbo.graphql_schemas.digests[_service]

# ── Rules declare their own scope ───────────────────────────────────────────

_rule_meta[rid] := meta if {
	some _, m in data.dvtp.gbo.rules
	rid := m.rule_id
	meta := {
		"covers_types": object.get(m, "covers_types", set()),
		"covers_fields": object.get(m, "covers_fields", set()),
		"spec": m.spec,
	}
}

# ── The regime decides which rules apply ────────────────────────────────────
# A consent token, verified or not, puts the request in the consent regime
# (pip.consent is set exactly then); without one, the PID regime. Only that
# regime's rules bind, plus rules naming no regime (test rules only).

_regime := "consent" if {
	object.get(_pip_obj, "consent", null) != null
} else := "pid"

_rule_regime(spec) := "consent" if {
	object.get(spec, "consent_required", false) == true
} else := "pid" if {
	object.get(spec, "pid_required", false) == true
} else := ""

_active_rules[rid] := m if {
	some rid, m in _rule_meta
	_rule_regime(m.spec) in {"", _regime}
}

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
	key := sprintf("%s.%s", [rf.parentType, rf.field])
	df := {
		"index": i,
		"id": concat(".", rf.path),
		"key": key,
		"record": rf,
		"policy_ids": _effective_policy_ids(rf, key),
	}
]

# ── Context for the rules ───────────────────────────────────────────────────
# lib.evaluate reads only this context, never input; the consent in it was
# verified by the policy itself (consent.rego). subject_placeholders are the
# names a consent-based query may give its subject.

_ctx := {
	"subject": input.subject,
	"time": object.get(input.context, "time", ""),
	"fields": object.get(_resource_attributes, ["graphql", "fields"], []),
	"resource": object.union(_resource_without_mapper, {"scope": _declared_scope, "subject_placeholders": _subject_placeholders}),
	"pip": _pip_obj,
}

# The evaluation context of the FTV profile (Section 9.4): every mapper
# record of the request as `fields` (__typename and introspection included),
# for checks that deny over several fields, and the resource without the
# mapper's output. GBO adds the declared scope and the subject placeholders.
_resource_attributes := object.get(object.get(input, "resource", {}), "attributes", {})

_resource_without_mapper := object.union(
	object.remove(object.get(input, "resource", {}), ["attributes"]),
	{"attributes": object.remove(_resource_attributes, ["graphql"])},
)

# The scope the consumer declares in X-GBO-Scope, header name in any case.
# Untrusted: the rules check it against the consent's scopes or their own.
# Two values give no scope rather than one picked: the pick would be
# arbitrary, and the source could act on the other.
_scope_values contains value if {
	some name, value in object.get(input.context, "headers", {})
	lower(name) == "x-gbo-scope"
	is_string(value)
}

_declared_scope := scope if {
	count(_scope_values) == 1
	some scope in _scope_values
} else := ""

# Without a verified consent the placeholders stand for nobody.
_subject_placeholders := lib.subject_placeholders if {
	object.get(object.get(_pip_obj, "consent", {}), "context_valid", false) == true
} else := set()

# A pip.consent or pip.integrator in input is dropped: the policy decides
# only on a consent it verified and admission data it pulled itself.
_pip_obj := object.union(_pip_with_consent, _pip_integrator)

_pip_with_consent := object.union(_input_pip, {"consent": consent.resolved}) if {
	consent.resolved
} else := _input_pip

_input_pip := object.remove(object.get(input.context, "pip", {}), ["consent", "integrator"])

# On a delegated call, the acting peer's entry in the admission register
# (data.entities, pulled by OpenFTV). Without one the mandate check fails.
_pip_integrator := {"integrator": entry} if {
	lib.delegated({"subject": input.subject})
	entry := data.entities.dvtp_participant[input.subject.id]
} else := {}

_eval(rid, field) := lib.evaluate(_rule_meta[rid].spec, object.union(_ctx, {"field": field}))

# Each bound rule evaluated once per data field. A partial rule is cached, a
# function call is not, and the per-field logic reads an outcome repeatedly.
_outcomes[i][rid] := _eval(rid, f.record) if {
	some f in _data_fields
	i := f.index
	some rid in f.policy_ids
}

# ── Per field: the first bound rule that allows grants ──────────────────────

_decide(f) := {"decision": false, "context": {"reason_admin": {"code": "NO_APPLICABLE_RULE", "evaluated": []}}} if {
	count(f.policy_ids) == 0
}

_decide(f) := _evaluate_field(f.policy_ids, _outcomes[f.index]) if {
	count(f.policy_ids) > 0
}

_evaluate_field(policy_ids, outcome) := result if {
	allowing := [rid | some rid in policy_ids; outcome[rid].decision == true]
	count(allowing) > 0
	rid := allowing[0]
	result := {"decision": true, "context": {
		"granted_by": rid,
		"granted_steps": _outcome_steps(outcome[rid]),
	}}
} else := result if {
	# No rule allowed. Each rule's code and steps form the trace; the
	# field's code follows the profile (Section 9.4): a server problem if any
	# rule hit one, else the first denying rule's code.
	evaluated := [{
		"rule": r,
		"code": _outcome_code(outcome[r]),
		"steps": _outcome_steps(outcome[r]),
	} |
		some r in policy_ids
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

# The consent register, or its keys, did not answer: a server problem, not
# the consumer's. The trace still shows which.
_pip_unavailable := {"CONSENT_STATUS_UNAVAILABLE", "CONSENT_KEYS_UNAVAILABLE"}

# A deny's steps are under context.reason_admin.steps, an allow's under steps.
_outcome_steps(outcome) := outcome.context.reason_admin.steps if {
	outcome.context.reason_admin.steps
} else := outcome.context.steps if {
	outcome.context.steps
} else := []

_outcome_code(outcome) := outcome.context.reason_admin.code if {
	outcome.context.reason_admin.code
} else := "UNKNOWN"
