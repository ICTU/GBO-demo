package dvtp.gbo

import data.dvtp.gbo.lib

# ═══════════════════════════════════════════════════════════════════════════
# GBO rule-engine PDP-runtime for DvTP (binding + decision + aggregation).
#
# Generic, rule-agnostic runtime of the FTV GraphQL profile's policy model
# (Section 9). The policy consists of the self-contained rules in
# ./rules/*.rego (pure data: rule_id + covers_types + covers_fields + spec).
# The runtime reads the field list the mapper adds to the request
# (input.resource.attributes.graphql, Section 6.2), binds each data field to
# the applicable rules, evaluates those rules via lib.evaluate(spec, ctx)
# with the field's own record, and aggregates into ONE decision = the AND
# across all data fields, with the per-field detail in decision.context.
#
# The consent is resolved by the policy itself (consent.rego); the scope is
# the X-GBO-Scope header the consumer declares, which the rules validate.
# ═══════════════════════════════════════════════════════════════════════════

import data.dvtp.gbo.consent

# ── Entrypoint: one Decision = closed-world AND across all requested data fields ─

_field_decisions := [{"field": f.id, "key": f.key, "index": f.index, "result": _decide(f)} | some f in _data_fields]

# The resolved consent goes back into the response document. It is not in
# input — the policy fetched it — so without this the decision log would
# record a decision without the attribute it was taken on (#330, #332).
response := object.union(_decision, {"context": {"pip": {"consent": consent.resolved}}}) if {
	consent.resolved
} else := _decision

_decision := {"decision": false, "context": {"reason_admin": _request_failure}} if {
	_request_failure
} else := {"decision": true, "context": {"granted": granted}} if {
	count(_field_decisions) > 0
	every fd in _field_decisions {
		fd.result.decision == true
	}
	granted := [{
		"field": fd.field,
		"key": fd.key,
		"rule": fd.result.context.granted_by,
		"steps": object.get(fd.result.context, "granted_steps", []),
	} |
		some fd in _field_decisions
	]
} else := {"decision": false, "context": deny_ctx} if {
	count(_field_decisions) > 0
	denied := [{
		"field": fd.field,
		"key": fd.key,
		"index": fd.index,
		"code": fd.result.context.reason_admin.code,
		"evaluated": fd.result.context.reason_admin.evaluated,
	} |
		some fd in _field_decisions
		fd.result.decision == false
	]
	count(denied) > 0
	deny_ctx := {
		"denied_fields": denied,
		"reason_admin": {"code": _request_reason(denied)},
	}
} else := {"decision": false, "context": {"reason_admin": {"code": "NO_APPLICABLE_RULE"}}}

# ── The mapper's output, checked before any rule (Section 9.5) ───────────────
# In this order: the output is there, its schema is the one the bundle pins
# for the service, the mapper could verify the request, and the request asks
# for at least one data field. Any failure denies the request as a whole.

_profile := "ftv-graphql/0.1"

_gql := input.resource.attributes.graphql

_service := object.get(object.get(input.subject, "attributes", {}), "service_name", "")

_request_failure := {"code": "CONFIG_ERROR", "subcode": "MAPPER_OUTPUT_MISSING"} if {
	not _gql.profile == _profile
} else := {"code": "CONFIG_ERROR", "subcode": "SCHEMA_MISMATCH"} if {
	_gql.schema != null
	not _schema_pinned
} else := object.filter(_gql.unverifiable, {"code", "subcode"}) if {
	_gql.unverifiable != null
} else := {"code": "NO_DATA_FIELDS"} if {
	count(_data_fields) == 0
}

# The schemas travel with the PDP image; the bundle pins each service's
# digest (graphql_schemas.rego). A schema without a pin is not pinned.
_schema_pinned if _gql.schema.digest == data.dvtp.gbo.graphql_schemas.digests[_service]

# ── Binding: self-contained rules declare their scope in policy-as-code ──────

_rule_meta[rid] := meta if {
	some _, m in data.dvtp.gbo.rules
	rid := m.rule_id
	meta := {
		"covers_types": object.get(m, "covers_types", set()),
		"covers_fields": object.get(m, "covers_fields", set()),
		"spec": m.spec,
	}
}

# ── The regime decides which rules apply ─────────────────────────────────────
# A consent token puts the request in the consent regime, its absence in
# the PID regime: pip.consent is present exactly when the request carried a
# token, verified or not. Only the rules of that regime are bound, plus any
# rule that names no regime (a test rule; every GBO rule names one). So a
# consent request is never judged by a PID rule, and a field outside the
# regime's rules has no applicable rule.

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

# Effective ruleset per data field, by its key ParentType.field (Section 9.3):
#   1. rules that name the key → those rules;
#   2. otherwise a leaf without arguments, outside the root type → the
#      rules that cover its parent type;
#   3. otherwise → none → NO_APPLICABLE_RULE.
# A field with arguments, a root field and an edge are only ever bound by
# their own key: a rule bound through a type does not see the arguments,
# and would cover entry points added to the schema later.
default _effective_policy_ids(_, _) := []

_effective_policy_ids(_, key) := _field_rules(key) if _field_declared[key]

_effective_policy_ids(rf, key) := _type_rules(rf.parentType) if {
	not _field_declared[key]
	rf.leaf == true
	not rf.args
	not rf.parentType in _root_types
}

# ── Data fields with ruleset (closed-world) ──────────────────────────────────
# Every record but __typename and the inside of introspection (Section 9.2).
# Root fields are data fields: they run a resolver with their arguments.
# Each record is its own decision, however many share a key or a path.

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

# ── Context for the rules ──────────────────────────────────────────────────
# Contains consent-PIP + resource so lib.evaluate can perform consent-checks
# without reading input.* itself (dependency-injection style). The consent
# is verified and status-checked per evaluation by the policy itself
# (consent.rego, via http.send). The PID regime adds nothing to the PIP:
# its rules read the request itself (#364).
#
# resource.subject_placeholders are what a consent-based query may name its
# subject with. They are set here, so a rule's constraint-binding checks the
# query's subject argument against them.

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

# The scope the consumer declares in X-GBO-Scope, in whatever case the PEP
# forwarded the header name. Untrusted: the rules check it against the
# consent's scopes or their own. More than one value is not resolved by
# picking one.
_scope_values contains value if {
	some name, value in object.get(input.context, "headers", {})
	lower(name) == "x-gbo-scope"
	is_string(value)
}

_declared_scope := scope if {
	count(_scope_values) == 1
	some scope in _scope_values
} else := ""

# The placeholders stand for the subject of a verified consent. Without one
# they stand for nobody, and no query argument matches them.
_subject_placeholders := lib.subject_placeholders if {
	object.get(object.get(_pip_obj, "consent", {}), "context_valid", false) == true
} else := set()

# The PIP attributes the rules see. A pip.consent or pip.integrator
# arriving in input is dropped, never trusted: nothing upstream is meant to
# set either, and the policy decides only on a consent it verified itself
# and on admission data it pulled itself.
_pip_obj := object.union(_pip_with_consent, _pip_integrator)

_pip_with_consent := object.union(_input_pip, {"consent": consent.resolved}) if {
	consent.resolved
} else := _input_pip

_input_pip := object.remove(object.get(input.context, "pip", {}), ["consent", "integrator"])

# On a delegated call, the acting peer's entry in the DvTP admission
# register: whether it is active, and for which service providers and rules
# it may act (acts_for). OpenFTV pulls the register into data.entities, the
# same feed contract autosign reads. A peer without an entry gets none, and
# the mandate axis fails closed on it.
_pip_integrator := {"integrator": entry} if {
	lib.delegated({"subject": input.subject})
	entry := data.entities.dvtp_participant[input.subject.id]
} else := {}

# ── Per-rule evaluation (given field) ────────────────────────────────────────

_eval(rid, field) := lib.evaluate(_rule_meta[rid].spec, object.union(_ctx, {"field": field}))

# Every bound rule evaluated once per data field, by field index. A partial
# rule is cached within the evaluation; a function call is not, and the
# per-field logic below reads an outcome several times.
_outcomes[i][rid] := _eval(rid, f.record) if {
	some f in _data_fields
	i := f.index
	some rid in f.policy_ids
}

# ── Per-field evaluation: the first bound rule that allows grants ────────────

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
	# No rule allowed: aggregate reason_admin with the worst code and
	# carry per-rule steps so the UI can show the cascade-trace (pass/
	# fail/skipped per axis) for each evaluated rule.
	evaluated := [{
		"rule": r,
		"code": _outcome_code(outcome[r]),
		"steps": _outcome_steps(outcome[r]),
	} |
		some r in policy_ids
	]
	result := {
		"decision": false,
		"context": {"reason_admin": {"code": _worst_code(evaluated), "evaluated": evaluated}},
	}
}

# Steps live in different places depending on ALLOW/DENY:
#   - DENY: lib emits them under context.reason_admin.steps
#   - ALLOW: lib emits them under context.steps (no reason_admin on ALLOW)
_outcome_steps(outcome) := outcome.context.reason_admin.steps if {
	outcome.context.reason_admin.steps
} else := outcome.context.steps if {
	outcome.context.steps
} else := []

# ── Deny-aggregation helpers (DvTP-specific priority) ────────────────────────
# Severity order: system errors before policy-DENY, and within policy-DENY
# deeper causes (no consent) before derived ones (scope/fields).

_code_priority("CONSENT_NOT_FOUND") := 60

# Token verification failures. The signed-context axis reports one of these
# in place of the generic CONSENT_CONTEXT_INVALID when consent.rego can say
# which check failed. Only one arises per request; distinct values keep
# _worst_code deterministic regardless.
_code_priority("CONSENT_SIGNATURE_INVALID") := 73

_code_priority("CONSENT_TOKEN_EXPIRED") := 72

_code_priority("CONSENT_KEYS_UNAVAILABLE") := 71

_code_priority("CONSENT_CONTEXT_INVALID") := 70

_code_priority("CONSENT_STATUS_UNAVAILABLE") := 69

_code_priority("CONSENT_ACTOR_MISMATCH") := 68

# A delegated call from a peer with no mandate for the represented party
# and rule. Structural, like ACTOR_NOT_ALLOWED; below the consent binding,
# which names the deeper cause when the integrator acts for the wrong party.
_code_priority("INTEGRATOR_NOT_REGISTERED") := 66

_code_priority("CONSENT_WITHDRAWN") := 50

_code_priority("CONSENT_EXPIRED") := 45

_code_priority("CONSENT_SCOPE_MISMATCH") := 40

# Per-year coverage: a requested belastingjaar without a matching
# bd:ib:<year> scope. Sits next to CONSENT_SCOPE_MISMATCH (same severity
# class: a scope/authorization gap, not a system error) but must be a
# distinct priority so _worst_code stays deterministic when both fire.
_code_priority("YEAR_NOT_COVERED") := 41

_code_priority("YEAR_NOT_ALLOWED") := 63

_code_priority("CONSTRAINT_MISMATCH") := 30

# EUDI-specific: higher than other policy-checks because without BSN
# there is no flow.
_code_priority("PID_NOT_PRESENT") := 55

# EUDI-specific: scope- and actor-authorization are more structural than
# pid-format errors — if scope or actor is not allowed, the rule is
# fundamentally not applicable. Higher than PID_NOT_PRESENT so the reason
# shows the deeper cause. Both must be unique relative to the consent-
# priorities — otherwise _worst_code conflicts when multiple rules with
# different axes fail on the same field.
_code_priority("ACTOR_NOT_ALLOWED") := 65

_code_priority("SCOPE_NOT_ALLOWED") := 62

# NO_APPLICABLE_RULE — the engine's closed-world default when no rule
# covers a field. Under model C this is how field-out-of-coverage
# manifests: the rule's covers_fields IS the catalog; anything outside
# falls here.
_code_priority("NO_APPLICABLE_RULE") := 25

default _code_priority(_) := 5

# The reason for the request as a whole: the worst of its fields' reasons,
# with one exception. In the PID regime the subject is named on one field
# (the root field's argument) and the other fields are judged without it.
# When no subject is named, the other fields fail on whatever comes next
# (the actor, the year), but those failures belong to a request about
# nobody: the missing subject is the reason.
_request_reason(denied) := "PID_NOT_PRESENT" if {
	_regime == "pid"
	some d in denied
	d.code == "PID_NOT_PRESENT"
} else := _worst_code([{"code": d.code} | some d in denied])

_worst_code(evaluated) := code if {
	some i in numbers.range(0, count(evaluated) - 1)
	code := evaluated[i].code
	prio := _code_priority(code)
	every j in numbers.range(0, count(evaluated) - 1) {
		_code_priority(evaluated[j].code) <= prio
	}
} else := "NO_APPLICABLE_RULE"

_outcome_code(outcome) := outcome.context.reason_admin.code if {
	outcome.context.reason_admin.code
} else := "UNKNOWN"
