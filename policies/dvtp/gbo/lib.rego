package dvtp.gbo.lib

# ═══════════════════════════════════════════════════════════════════════════
# DvTP evaluation library for GBO rule-engine rules.
#
# Consent-driven primitives (vs. iWlz' role-driven equivalent): each rule-
# spec describes which consent-checks must hold for the current field. The
# engine calls evaluate(spec, ctx) per field; ctx.field carries the field
# the check applies to (needed for field-in-consent).
#
# Cascade-output: evaluate emits ALL checks with their status (pass / fail /
# skipped). The first failing check wins as the DENY-reason; subsequent
# checks are "skipped" (short-circuit semantics). This gives the UI a real
# trace that can be rendered like iWlz' "Show PDP evaluation (N steps)"
# instead of only the final code.
#
# ctx-shape (provided by the dvtp.gbo engine):
#   ctx := {
#     "subject":  { ...AuthZEN subject (unused in DVT0001 but available
#                   for future role-checks) },
#     "args":     { "vars.<name>": value, "input.<name>": value, ... },
#     "time":     "<RFC3339>",
#     "resource": { "scope": "...", "consent_id": "...", "consented_fields": [...] },
#     "pip":      { "consent": { "exists": bool, "withdrawn": bool,
#                                "valid_until": "<RFC3339>",
#                                "granted_scopes": [...] } },
#     "field":    "Query.<path>.<name>"
#   }
#
# evaluate(spec, ctx) returns:
#   ALLOW: {"decision": true,  "context": {"steps": [{code, status, label, expected, actual?}, ...]}}
#   DENY:  {"decision": false, "context": {"reason_admin": {
#            "code": "<first-fail>",
#            "rule": "<spec.rule_id>",
#            "expected": "<first-fail.expected>",
#            "steps": [...]}}}
# ═══════════════════════════════════════════════════════════════════════════

evaluate(spec, ctx) := result if {
	steps := _steps_with_short_circuit(spec, ctx)
	failing := [s | some s in steps; s.status == "fail"]
	count(failing) == 0
	result := {"decision": true, "context": {"steps": steps}}
} else := result if {
	steps := _steps_with_short_circuit(spec, ctx)
	failing := [s | some s in steps; s.status == "fail"]
	count(failing) > 0
	first := failing[0]
	result := {
		"decision": false,
		"context": {"reason_admin": {
			"code": first.code,
			"rule": spec.rule_id,
			"expected": first.expected,
			"steps": steps,
		}},
	}
}

# ── Cascade: raw step-list + short-circuit modifier ──────────────────────────
# All checks are first evaluated independently (pass/fail). Then we mark all
# checks AFTER the first fail as "skipped" — this fits the semantics that a
# DENY truncates the cascade, and the iWlz rendering that shows "skipped" as
# a separate status.

_raw_steps(spec, ctx) := [
	_check_consent_exists(spec, ctx),
	_check_consent_not_withdrawn(spec, ctx),
	_check_consent_not_expired(spec, ctx),
	_check_consent_covers_scope(spec, ctx),
	_check_constraint(spec, ctx),
	_check_pid_present(spec, ctx),
	_check_scope_allowed(spec, ctx),
	_check_actor_allowed(spec, ctx),
]

_steps_with_short_circuit(spec, ctx) := result if {
	raw := _raw_steps(spec, ctx)
	first_fail_idx := _first_fail_index(raw)
	first_fail_idx >= 0
	result := [_skip_after(raw[i], i, first_fail_idx) | some i in numbers.range(0, count(raw) - 1)]
} else := _raw_steps(spec, ctx)

_first_fail_index(steps) := idx if {
	some i in numbers.range(0, count(steps) - 1)
	steps[i].status == "fail"
	idx := i
	every j in numbers.range(0, i - 1) {
		steps[j].status != "fail"
	}
} else := -1

# Steps after the first fail get status "skipped" (= no longer evaluated
# because an earlier axis already denied). Steps up to and including the
# fail keep their original status.
_skip_after(step, i, fail_idx) := object.union(step, {"status": "skipped"}) if i > fail_idx

_skip_after(step, i, fail_idx) := step if i <= fail_idx

# ── The consent-axes as _check_*(spec, ctx) → {code, status, label, expected} ─
# Each check has a PASS-clause (the condition holds → step.status="pass"),
# a FAIL-clause (the condition does not hold → step.status="fail"), and a
# SKIPPED-clause (the spec does not activate this check → step.status="skipped").
# Verbose but unavoidable in Rego — boolean-rules are undefined-when-false
# and cannot be passed directly as function-arg.

_check_consent_exists(spec, ctx) := step if {
	spec.consent_required
	ctx.pip.consent.exists == true
	step := _step("CONSENT_NOT_FOUND", "Consent exists in PIP", "consent.exists == true", "pass")
} else := step if {
	spec.consent_required
	not ctx.pip.consent.exists == true
	step := _step("CONSENT_NOT_FOUND", "Consent exists in PIP", "consent.exists == true", "fail")
} else := _step_skipped("CONSENT_NOT_FOUND", "Consent exists in PIP", "n/a")

_check_consent_not_withdrawn(spec, ctx) := step if {
	spec.consent_required
	ctx.pip.consent.withdrawn == false
	step := _step("CONSENT_WITHDRAWN", "Consent not withdrawn", "consent.withdrawn == false", "pass")
} else := step if {
	spec.consent_required
	ctx.pip.consent.withdrawn == true
	step := _step("CONSENT_WITHDRAWN", "Consent not withdrawn", "consent.withdrawn == false", "fail")
} else := _step_skipped("CONSENT_WITHDRAWN", "Consent not withdrawn", "n/a")

_check_consent_not_expired(spec, ctx) := step if {
	spec.consent_required
	within_validity_window(ctx)
	step := _step("CONSENT_EXPIRED", "Consent within validity window", sprintf("now < %s", [object.get(ctx.pip.consent, "valid_until", "?")]), "pass")
} else := step if {
	spec.consent_required
	not within_validity_window(ctx)
	step := _step("CONSENT_EXPIRED", "Consent within validity window", sprintf("now < %s", [object.get(ctx.pip.consent, "valid_until", "?")]), "fail")
} else := _step_skipped("CONSENT_EXPIRED", "Consent within validity window", "n/a")

_check_consent_covers_scope(spec, ctx) := step if {
	spec.consent_must_cover_scope
	consent_covers_scope(ctx)
	step := _step("CONSENT_SCOPE_MISMATCH", "Scope covered by consent", sprintf("%q in consent.granted_scopes", [object.get(ctx.resource, "scope", "")]), "pass")
} else := step if {
	spec.consent_must_cover_scope
	not consent_covers_scope(ctx)
	step := _step("CONSENT_SCOPE_MISMATCH", "Scope covered by consent", sprintf("%q in consent.granted_scopes", [object.get(ctx.resource, "scope", "")]), "fail")
} else := _step_skipped("CONSENT_SCOPE_MISMATCH", "Scope covered by consent", "n/a")

_check_constraint(spec, ctx) := step if {
	# All constraint-bindings must be satisfied (AND). For multi-binding
	# we report the FIRST unsatisfied binding as the expected-string so
	# that the error message stays specific. For ALL-pass we show the
	# number of bindings that were verified.
	bindings := object.get(spec, "constraint_binding", [])
	count(bindings) > 0
	failing := [fm | some fm in bindings; not constraint_binding_satisfied(fm, ctx)]
	count(failing) == 0
	step := _step("CONSTRAINT_MISMATCH", "Constraint-binding satisfied", sprintf("%d binding(s) satisfied", [count(bindings)]), "pass")
} else := step if {
	bindings := object.get(spec, "constraint_binding", [])
	count(bindings) > 0
	failing := [fm | some fm in bindings; not constraint_binding_satisfied(fm, ctx)]
	count(failing) > 0
	first := failing[0]
	step := _step("CONSTRAINT_MISMATCH", "Constraint-binding satisfied", sprintf("%s == resource.%s", [first.arg, first.resource_field]), "fail")
} else := _step_skipped("CONSTRAINT_MISMATCH", "Constraint-binding satisfied", "no constraint configured")

# EUDI-flow: validate that the adapter provided a BSN from the wallet's
# PID-disclosure. In V1 the adapter itself performs no crypto-check on
# the PID-signature. This axis only checks presence + minimal shape
# (9 digits).
_check_pid_present(spec, ctx) := step if {
	spec.pid_required
	bsn := object.get(object.get(ctx.pip, "pid", {}), "bsn", "")
	bsn != ""
	regex.match("^[0-9]{9}$", bsn)
	step := _step("PID_NOT_PRESENT", "PID disclosed by wallet", "input.pip.pid.bsn ~ /^[0-9]{9}$/", "pass")
} else := step if {
	spec.pid_required
	bsn := object.get(object.get(ctx.pip, "pid", {}), "bsn", "")
	not regex.match("^[0-9]{9}$", bsn)
	step := _step("PID_NOT_PRESENT", "PID disclosed by wallet", "input.pip.pid.bsn ~ /^[0-9]{9}$/", "fail")
} else := _step_skipped("PID_NOT_PRESENT", "PID disclosed by wallet", "n/a (no PID-flow)")

# Scope-authorization: only active when the rule explicitly declares an
# allowed_scopes set. Without this axis a rule would only be scoped via
# covers_fields (implicitly through the engine's closed-world), and any
# arbitrary resource.scope-string could pass through — the authoritative
# source of scope per policy-path would be missing. DvTP covers this via
# consent-scope-cover; EUDI via a rule-declared whitelist.
_check_scope_allowed(spec, ctx) := step if {
	allowed := object.get(spec, "allowed_scopes", set())
	count(allowed) > 0
	scope := object.get(ctx.resource, "scope", "")
	scope in allowed
	step := _step("SCOPE_NOT_ALLOWED", "Scope allowed for rule", sprintf("%q in spec.allowed_scopes", [scope]), "pass")
} else := step if {
	allowed := object.get(spec, "allowed_scopes", set())
	count(allowed) > 0
	scope := object.get(ctx.resource, "scope", "")
	not scope in allowed
	step := _step("SCOPE_NOT_ALLOWED", "Scope allowed for rule", sprintf("%q in spec.allowed_scopes", [scope]), "fail")
} else := _step_skipped("SCOPE_NOT_ALLOWED", "Scope allowed for rule", "no scope-whitelist configured")

# Actor-authorization: only explicitly designated actors may trigger this
# rule. Supports e.g. eIDAS art. 5a-style designation: one designated
# EDI-issuer per attestation-type. Without this axis, any OIN that passes
# the FSC-transport (grant + inway) could trigger the rule; with this
# axis there is a separate policy-check that the actor is also allowed
# at rule-level.
_check_actor_allowed(spec, ctx) := step if {
	allowed := object.get(spec, "allowed_actors", set())
	count(allowed) > 0
	actor := object.get(ctx.subject, "id", "")
	actor in allowed
	step := _step("ACTOR_NOT_ALLOWED", "Actor allowed for rule", sprintf("%q in spec.allowed_actors", [actor]), "pass")
} else := step if {
	allowed := object.get(spec, "allowed_actors", set())
	count(allowed) > 0
	actor := object.get(ctx.subject, "id", "")
	not actor in allowed
	step := _step("ACTOR_NOT_ALLOWED", "Actor allowed for rule", sprintf("%q in spec.allowed_actors", [actor]), "fail")
} else := _step_skipped("ACTOR_NOT_ALLOWED", "Actor allowed for rule", "no actor-whitelist configured")

_step(code, label, expected, status) := {
	"code": code,
	"label": label,
	"expected": expected,
	"status": status,
}

_step_skipped(code, label, expected) := _step(code, label, expected, "skipped")

# ── Consent-scope-check ──────────────────────────────────────────────────────

consent_covers_scope(ctx) if {
	some s in ctx.pip.consent.granted_scopes
	s == ctx.resource.scope
}

# ── Constraint-binding-check ─────────────────────────────────────────────────

constraint_binding_satisfied(fm, ctx) if {
	arg_value := ctx.args[fm.arg]
	res_value := object.get(ctx.resource, fm.resource_field, "")
	arg_value == res_value
}

# ── Field-in-consent-check ───────────────────────────────────────────────────

field_in_consent(ctx) if {
	leaf := last_segment(ctx.field)
	some f in object.get(ctx.resource, "consented_fields", [])
	f == leaf
}

field_is_scalar_leaf(ctx) if {
	some f in input.resolved.fields
	f.id == ctx.field
	f.scalar
}

last_segment(path) := segs[count(segs) - 1] if {
	segs := split(path, ".")
}

# ── Validity window ──────────────────────────────────────────────────────────

within_validity_window(ctx) if {
	now_ns := _now_ns(ctx)
	end_ns := time.parse_rfc3339_ns(ctx.pip.consent.valid_until)
	now_ns < end_ns
}

_now_ns(ctx) := time.parse_rfc3339_ns(ctx.time) if ctx.time != ""

_now_ns(ctx) := time.now_ns() if {
	not ctx.time
} else := time.now_ns() if ctx.time == ""
