package dvtp.gbo

import data.dvtp.gbo.lib

# ═══════════════════════════════════════════════════════════════════════════
# GBO rule-engine PDP-runtime for DvTP (binding + decision + aggregation).
#
# Generic, rule-agnostic runtime. The policy consists of the self-contained
# rules in ./rules/*.rego (pure data: rule_id + covers_types + covers_fields
# + spec). The runtime binds each requested field to the applicable rules,
# evaluates those rules via lib.evaluate(spec, ctx), and aggregates into
# ONE AuthZEN Decision (§6.2) = the AND across all covered fields, with
# the per-field detail in decision.context.
#
# Adapted for consent-based policies: ctx carries input.pip + input.resource
# so consent-checks (lib.evaluate) can access them, and _eval passes the
# current `field` so field-in-consent works per field.
# ═══════════════════════════════════════════════════════════════════════════

# ── Entrypoint: one Decision = closed-world AND across all requested data fields ─

_field_decisions := [{"field": f.id, "result": _decide(f)} | some f in _data_fields]

response := {"decision": false, "context": {"reason_admin": {"code": "COVERAGE_UNVERIFIABLE"}}} if {
	_coverage_unverifiable
} else := {"decision": true, "context": {"granted": granted}} if {
	count(_field_decisions) > 0
	every fd in _field_decisions {
		fd.result.decision == true
	}
	granted := [{
		"field": fd.field,
		"rule": fd.result.context.granted_by,
		"steps": object.get(fd.result.context, "granted_steps", []),
	} |
		some fd in _field_decisions
	]
} else := {"decision": false, "context": deny_ctx} if {
	count(_field_decisions) > 0
	denied := [{
		"field": fd.field,
		"code": fd.result.context.reason_admin.code,
		"evaluated": fd.result.context.reason_admin.evaluated,
	} |
		some fd in _field_decisions
		fd.result.decision == false
	]
	count(denied) > 0
	deny_ctx := {
		"denied_fields": denied,
		"reason_admin": {"code": _worst_code([{"code": d.code} | some d in denied])},
	}
} else := {"decision": false, "context": {"reason_admin": {"code": "NO_APPLICABLE_RULE"}}}

# Demo helper for the dev-portal UI render: per-field evaluations + the
# args supplied by the context-handler. A production PEP uses only
# `response`.
view := {
	"response": response,
	"evaluations": [_decide(f) | some f in _data_fields],
	"derived": {
		"evaluations": [_field_eval(f) | some f in _data_fields],
		"resolvedArguments": _args,
		"coverage_unverifiable": _coverage_unverifiable,
	},
}

default _coverage_unverifiable := false

_coverage_unverifiable if input.resolved.coverage_unverifiable

# ── Binding: self-contained rules declare their scope in policy-as-code ──────

_rule_meta[rid] := meta if {
	some _, m in data.dvtp.gbo.rules
	rid := m.rule_id
	meta := {
		"covers_types": object.get(m, "covers_types", set()),
		"covers_fields": object.get(m, "covers_fields", set()),
		"has_pip": object.get(m.spec, "pip", null) != null,
		"spec": m.spec,
	}
}

_field_declared contains key if {
	some _, m in _rule_meta
	some key in m.covers_fields
}

_field_rules(key) := [rid |
	some rid, m in _rule_meta
	key in m.covers_fields
]

_type_rules(t) := [rid |
	some rid, m in _rule_meta
	t in m.covers_types
]

# Effective ruleset per requested field:
#   1. field-level declared → those rules (override; scalars AND edges);
#   2. otherwise known scalar → inherit from parent type;
#   3. otherwise → empty → NO_APPLICABLE_RULE.
default _effective_policy_ids(_, _) := []

_effective_policy_ids(_, key) := _field_rules(key) if _field_declared[key]

_effective_policy_ids(rf, key) := _type_rules(rf.parent) if {
	not _field_declared[key]
	object.get(rf, "known", true)
	rf.scalar
}

# ── Data fields with ruleset (closed-world) ──────────────────────────────────

_root_types := {"Query", "Mutation", "Subscription"}

_data_fields := [df |
	some rf in input.resolved.fields
	not rf.parent in _root_types
	not startswith(rf.name, "__")
	key := sprintf("%s.%s", [rf.parent, rf.name])
	df := {
		"id": rf.id,
		"policy_ids": _effective_policy_ids(rf, key),
	}
]

_field_eval(f) := {"resource": {
	"type": "graphql_field",
	"id": f.id,
	"properties": {"policy_ids": f.policy_ids},
}}

_args := object.get(input.resolved, "args", {})

# ── Context for the rules ────────────────────────────────────────────────────
# Contains consent-PIP + resource so lib.evaluate can perform consent-checks
# without reading input.* itself (dependency-injection style).

_ctx := {
	"subject": input.subject,
	"args": _args,
	"time": object.get(object.get(input, "context", {}), "time", ""),
	"resource": input.resource,
	"pip": object.get(input, "pip", {}),
}

# ── Per-rule evaluation (given field) ────────────────────────────────────────

_eval(rid, field) := lib.evaluate(_rule_meta[rid].spec, object.union(_ctx, {"field": field}))

# ── Per-field evaluation: cheap-first, short-circuit, lazy PIP ───────────────

_cheap(policy_ids) := [r | some r in policy_ids; not _rule_meta[r].has_pip]

_pip(policy_ids) := [r | some r in policy_ids; _rule_meta[r].has_pip]

_decide(f) := {"decision": false, "context": {"reason_admin": {"code": "NO_APPLICABLE_RULE", "evaluated": []}}} if {
	count(f.policy_ids) == 0
}

_decide(f) := _evaluate_field(f.policy_ids, f.id) if {
	count(f.policy_ids) > 0
}

_evaluate_field(policy_ids, field) := result if {
	cheap := _cheap(policy_ids)
	allow_idxs := [i |
		some i in numbers.range(0, count(cheap) - 1)
		_eval(cheap[i], field).decision == true
	]
	count(allow_idxs) > 0
	first := min(allow_idxs)
	rid := cheap[first]
	result := {"decision": true, "context": {
		"granted_by": rid,
		"granted_steps": _outcome_steps(_eval(rid, field)),
	}}
} else := result if {
	pip := _pip(policy_ids)
	count(pip) >= 1
	_eval(pip[0], field).decision == true
	rid := pip[0]
	result := {"decision": true, "context": {
		"granted_by": rid,
		"granted_steps": _outcome_steps(_eval(rid, field)),
	}}
} else := result if {
	# No rule allowed: aggregate reason_admin with the worst code and
	# carry per-rule steps so the UI can show the cascade-trace (pass/
	# fail/skipped per axis) for each evaluated rule.
	cheap := _cheap(policy_ids)
	pip := _pip(policy_ids)
	evaluated := [{
		"rule": r,
		"code": _outcome_code(_eval(r, field)),
		"steps": _outcome_steps(_eval(r, field)),
	} |
		some r in array.concat(cheap, pip)
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

_code_priority("CONSENT_WITHDRAWN") := 50

_code_priority("CONSENT_EXPIRED") := 45

_code_priority("CONSENT_SCOPE_MISMATCH") := 40

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
