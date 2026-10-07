package dvtp.gbo.lib

# ═══════════════════════════════════════════════════════════════════════════
# Evaluation library for GBO rules.
#
# The engine calls evaluate(spec, ctx) for one rule on one requested field.
# The spec says which checks the rule requires; evaluate runs them in a fixed
# order and denies with the code of the first check that fails.
#
# The result also lists every check as a step (pass, fail or skipped), with
# every check after the first failure marked skipped. The decision does not
# need this list. It exists for the developer portal, a demo feature that
# shows per rule which checks passed, failed or were skipped.
#
# ctx, built by the dvtp.gbo engine:
#   subject   AuthZEN subject: id is the FSC peer that connects,
#             attributes.outway_delegator_peer_id the peer it connects for
#             on a delegated connection
#   time      RFC3339
#   resource  scope, and subject_placeholders: the placeholders that stand
#             for the subject of a verified consent
#   pip       consent (from data.dvtp.gbo.consent): context_valid,
#             invalid_code, status_available, exists, withdrawn, valid_until,
#             granted_scopes, dienstverlener_oin. integrator: the acting
#             peer's admission entry {active, acts_for}, on a delegated call
#   field     the mapper's record of this field: path, parentType, field,
#             leaf, args
#
# evaluate returns
#   allow: {"decision": true,  "context": {"steps": [...]}}
#   deny:  {"decision": false, "context": {"reason_admin":
#            {"code", "rule", "expected", "steps"}}}
# ═══════════════════════════════════════════════════════════════════════════

# The placeholders a consent-based query names its subject with. The consumer
# holds no identifier of the citizen; after an allow the source puts in their
# place its own value from the consent token: the BSN for identity, its own
# pseudonym of the citizen for pseudonym. A rule lists the ones its API takes.
identity_placeholder := "consent:identity"

pseudonym_placeholder := "consent:pseudonym"

subject_placeholders := {identity_placeholder, pseudonym_placeholder}

evaluate(spec, ctx) := _outcome(spec, _short_circuit(_raw_steps(spec, ctx)))

# The steps are passed in, computed once: Rego does not cache function
# results, so computing them per branch would double the work.
_outcome(spec, steps) := {"decision": true, "context": {"steps": steps}} if {
	every s in steps {
		s.status != "fail"
	}
} else := result if {
	failing := [s | some s in steps; s.status == "fail"]
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

# ── Cascade ──────────────────────────────────────────────────────────────────
# Each check is evaluated on its own; _short_circuit then marks every check
# after the first failure as skipped, so the portal trace stops where the
# decision did.

_raw_steps(spec, ctx) := [
	_check_consent_context_valid(spec, ctx),
	_check_consent_status_available(spec, ctx),
	_check_consent_exists(spec, ctx),
	_check_consent_not_withdrawn(spec, ctx),
	_check_consent_not_expired(spec, ctx),
	_check_consent_covers_scope(spec, ctx),
	_check_constraint(spec, ctx),
	_check_pid_present(spec, ctx),
	_check_scope_allowed(spec, ctx),
	_check_years_allowed(spec, ctx),
	_check_years_in_scopes(spec, ctx),
	_check_consent_actor_binding(spec, ctx),
	_check_integrator_mandate(spec, ctx),
	_check_actor_allowed(spec, ctx),
]

_short_circuit(raw) := result if {
	first_fail_idx := _first_fail_index(raw)
	first_fail_idx >= 0
	result := [_skip_after(raw[i], i, first_fail_idx) | some i in numbers.range(0, count(raw) - 1)]
} else := raw

_first_fail_index(steps) := min(fails) if {
	fails := [i | some i, step in steps; step.status == "fail"]
	count(fails) > 0
} else := -1

_skip_after(step, i, fail_idx) := object.union(step, {"status": "skipped"}) if i > fail_idx

_skip_after(step, i, fail_idx) := step if i <= fail_idx

# ── Checks: each _check_*(spec, ctx) returns one step ────────────────────────

_check_consent_exists(spec, ctx) := _axis(
	{"code": "CONSENT_NOT_FOUND", "label": "Consent exists in PIP", "expected": "consent.exists == true"},
	_flag(spec, "consent_required"),
	_consent(ctx, "exists", false) == true,
)

_check_consent_not_withdrawn(spec, ctx) := _axis(
	{"code": "CONSENT_WITHDRAWN", "label": "Consent not withdrawn", "expected": "consent.withdrawn == false"},
	_flag(spec, "consent_required"),
	_consent(ctx, "withdrawn", null) == false,
)

_check_consent_not_expired(spec, ctx) := _axis(
	{"code": "CONSENT_EXPIRED", "label": "Consent within validity window", "expected": sprintf("now < %s", [_consent(ctx, "valid_until", "?")])},
	_flag(spec, "consent_required"),
	within_validity_window(ctx),
)

_check_consent_covers_scope(spec, ctx) := _axis(
	{"code": "CONSENT_SCOPE_MISMATCH", "label": "Scope covered by consent", "expected": sprintf("%q in consent.granted_scopes", [_scope(ctx)])},
	_flag(spec, "consent_must_cover_scope"),
	consent_covers_scope(ctx),
)

_check_constraint(spec, ctx) := step if {
	# Every binding on this field must hold; a failure names the first one
	# that does not.
	bindings := _bound_constraints(spec, ctx)
	count(bindings) > 0
	failing := [fm | some fm in bindings; not constraint_binding_satisfied(fm, ctx)]
	count(failing) == 0
	step := _step("CONSTRAINT_MISMATCH", "Constraint-binding satisfied", sprintf("%d binding(s) satisfied", [count(bindings)]), "pass")
} else := step if {
	bindings := _bound_constraints(spec, ctx)
	count(bindings) > 0
	failing := [fm | some fm in bindings; not constraint_binding_satisfied(fm, ctx)]
	count(failing) > 0
	first := failing[0]
	step := _step("CONSTRAINT_MISMATCH", "Constraint-binding satisfied", sprintf("%s.%s in %v", [first.field, first.arg, sort(first.placeholders)]), "fail")
} else := _step_skipped("CONSTRAINT_MISMATCH", "Constraint-binding satisfied", _constraint_skip_reason(spec))

_bound_constraints(spec, ctx) := [fm | some fm in object.get(spec, "constraint_binding", []); bound_here(fm, ctx)]

_constraint_skip_reason(spec) := "not on this field" if {
	count(object.get(spec, "constraint_binding", [])) > 0
} else := "no constraint configured"

# PID regime: no consent token, and a subject named in the rule's
# subject_argument, where the EUDI adapter puts the BSN from the wallet's PID
# disclosure. The PID itself is not verified here; the adapter trusts it.
#
# The no-token half keeps the regimes apart: pip.consent is present whenever
# a consent token came along, verified or not, and both regimes name a
# subject. It holds on every field; the subject half only on the field that
# carries the argument, and the AND across fields covers the rest.
_check_pid_present(spec, ctx) := _axis(
	{"code": "PID_NOT_PRESENT", "label": _pid_label, "expected": _pid_expected(spec), "skipped": "n/a (no PID-flow)"},
	_flag(spec, "pid_required"),
	_pid_regime(spec, ctx),
)

_pid_label := "PID regime: no consent token, subject named"

_pid_expected(spec) := sprintf("no consent token, and %s.%s named", [spec.subject_argument.field, spec.subject_argument.arg]) if {
	spec.subject_argument
} else := "no consent token, and a subject_argument declared by the rule"

default _pid_regime(_, _) := false

_pid_regime(spec, ctx) if {
	object.get(object.get(ctx, "pip", {}), "consent", null) == null
	_subject_named(spec, ctx)
}

# A placeholder names nobody here: it stands for the subject of a consent,
# and in the PID regime there is none.
_subject_named(spec, ctx) if {
	bound_here(spec.subject_argument, ctx)
	value := argument(spec.subject_argument, ctx)
	is_string(value)
	value != ""
	not value in subject_placeholders
}

# On another field the subject is not this field's to name. A rule that
# declares no subject_argument has no field that names one: it fails.
_subject_named(spec, ctx) if {
	spec.subject_argument
	not bound_here(spec.subject_argument, ctx)
}

# Active only when the rule declares allowed_scopes. It pins the scopes a
# rule accepts, which consent-scope coverage alone does not: a token for
# another scope, sent with that scope, covers it.
_check_scope_allowed(spec, ctx) := _axis(
	{"code": "SCOPE_NOT_ALLOWED", "label": "Scope allowed for rule", "expected": sprintf("%q in spec.allowed_scopes", [_scope(ctx)]), "skipped": "no scope-whitelist configured"},
	count(object.get(spec, "allowed_scopes", set())) > 0,
	_scope(ctx) in object.get(spec, "allowed_scopes", set()),
)

# Every requested year must be in the rule's own allowed_years. No scope is
# involved.
_check_years_allowed(spec, ctx) := _years_step(
	{"code": "YEAR_NOT_ALLOWED", "label": "Requested years allowed for rule", "passed": "allowed", "missing": "%v in spec.allowed_years", "skipped": "no year-whitelist configured"},
	count(object.get(spec, "allowed_years", set())) > 0,
	spec, ctx,
	[y | some y in _requested_years(spec, ctx); not sprintf("%v", [y]) in {sprintf("%v", [a]) | some a in object.get(spec, "allowed_years", set())}],
)

# Every requested year needs a bd:ib:<year> scope among the available
# scopes. The source returns every year for a person unless the query
# filters, so per-year authorization holds only when the filter is in the
# query: a missing filter fails closed.
_check_years_in_scopes(spec, ctx) := _years_step(
	{"code": "YEAR_NOT_COVERED", "label": "Requested years covered by scopes", "passed": "covered", "missing": "bd:ib:%v in scopes", "skipped": "n/a"},
	_flag(spec, "years_in_scopes"),
	spec, ctx,
	[y | some y in _requested_years(spec, ctx); not sprintf("bd:ib:%v", [y]) in _available_scopes(spec, ctx)],
)

# Active only when the rule declares allowed_actors: only designated parties
# may use the rule (as in eIDAS art. 5a designation of issuers), on top of
# what the FSC grant admits.
_check_actor_allowed(spec, ctx) := _axis(
	{"code": "ACTOR_NOT_ALLOWED", "label": "Actor allowed for rule", "expected": sprintf("%q in spec.allowed_actors", [acting_party(ctx)]), "skipped": "no actor-whitelist configured"},
	count(object.get(spec, "allowed_actors", set())) > 0,
	acting_party(ctx) in object.get(spec, "allowed_actors", set()),
)

# ── Acting and represented party (FSC delegation) ──────────────────────────
# A service provider calls a source itself, or through an integrator under a
# DelegatedServiceConnection grant. The Inway then gives the integrator as
# subject.id and the provider as outway_delegator_peer_id. Both come from a
# token the source's own Manager signed, on a contract all three parties
# signed, so the delegator is as trustworthy as subject.id.
#
# Consent binds the represented party: the provider the citizen consented
# to. The acting party needs a mandate of its own; consent gives it none.

acting_party(ctx) := object.get(ctx.subject, "id", "")

represented_party(ctx) := delegator if {
	delegator := object.get(object.get(ctx.subject, "attributes", {}), "outway_delegator_peer_id", "")
	is_string(delegator)
	delegator != ""
} else := acting_party(ctx)

default delegated(_) := false

delegated(ctx) if represented_party(ctx) != acting_party(ctx)

_check_consent_actor_binding(spec, ctx) := _axis(
	{"code": "CONSENT_ACTOR_MISMATCH", "label": _binding_label, "expected": _binding_expected},
	_flag(spec, "consent_actor_binding"),
	consent_actor_matches(ctx),
)

_binding_label := "Represented party matches signed consent recipient"

_binding_expected := "(FSC delegator, else subject.id) == consent.dienstverlener_oin"

default consent_actor_matches(_) := false

consent_actor_matches(ctx) if {
	party := represented_party(ctx)
	consent_actor := object.get(ctx.pip.consent, "dienstverlener_oin", "")
	party != ""
	party == consent_actor
}

# Every delegated call needs a mandate for this provider and this rule,
# whatever the rule declares. A direct call has no integrator to check.
_check_integrator_mandate(spec, ctx) := _axis(
	{"code": "INTEGRATOR_NOT_REGISTERED", "fail_code": _mandate_fail_code(ctx), "label": _mandate_label, "expected": _mandate_expected(spec, ctx), "skipped": "n/a (direct call)"},
	delegated(ctx),
	integrator_mandated(spec, ctx),
)

# Without the admission register no mandate can be checked: a PIP failure.
_mandate_fail_code(ctx) := "INTEGRATOR_REGISTER_UNAVAILABLE" if {
	object.get(ctx, ["pip", "integrator_register"], "") == "unavailable"
} else := "INTEGRATOR_NOT_REGISTERED"

_mandate_label := "Integrator registered for represented party and rule"

_mandate_expected(spec, ctx) := sprintf("%q active, acting for %q in %s", [acting_party(ctx), represented_party(ctx), spec.rule_id])

default integrator_mandated(_, _) := false

integrator_mandated(spec, ctx) if {
	integrator := object.get(object.get(ctx, "pip", {}), "integrator", {})
	integrator.active == true
	some mandate in object.get(integrator, "acts_for", [])
	mandate.peer_id == represented_party(ctx)
	spec.rule_id in mandate.rules
}

_step(code, label, expected, status) := {
	"code": code,
	"label": label,
	"expected": expected,
	"status": status,
}

_step_skipped(code, label, expected) := _step(code, label, expected, "skipped")

# A step from a check: skipped when the rule does not declare it, otherwise
# pass or fail. active and holds must be true or false, never undefined: an
# undefined argument would drop the step.
_axis(check, active, holds) := _step(check.code, check.label, check.expected, "pass") if {
	active
	holds
} else := _step(object.get(check, "fail_code", check.code), check.label, check.expected, "fail") if {
	active
} else := _step_skipped(check.code, check.label, object.get(check, "skipped", "n/a"))

# The year checks have a third outcome: no years in the query, which fails
# closed because the source would return every year.
_years_step(check, active, spec, ctx, missing) := _step_skipped(check.code, check.label, "not on this field") if {
	active
	not _years_apply(spec, ctx)
} else := _step(check.code, check.label, sprintf("%d year(s) %s", [count(_requested_years(spec, ctx)), check.passed]), "pass") if {
	active
	count(_requested_years(spec, ctx)) > 0
	count(missing) == 0
} else := _step(check.code, check.label, sprintf(check.missing, [missing[0]]), "fail") if {
	active
	count(_requested_years(spec, ctx)) > 0
} else := _step(check.code, check.label, "belastingjaren filter present in query", "fail") if {
	active
} else := _step_skipped(check.code, check.label, check.skipped)

_flag(spec, name) := object.get(spec, name, false) == true

_consent(ctx, name, default_value) := object.get(ctx, ["pip", "consent", name], default_value)

_scope(ctx) := object.get(object.get(ctx, "resource", {}), "scope", "")

# ── Consent scope ────────────────────────────────────────────────────────────

default consent_covers_scope(_) := false

consent_covers_scope(ctx) if {
	some s in ctx.pip.consent.granted_scopes
	s == ctx.resource.scope
}

# ── Constraint binding ───────────────────────────────────────────────────────

# The argument must be a placeholder the binding lists, and one that stands
# for someone: without a verified consent resource.subject_placeholders is
# empty, so nothing matches.
constraint_binding_satisfied(fm, ctx) if {
	value := argument(fm, ctx)
	value in fm.placeholders
	value in object.get(ctx.resource, "subject_placeholders", set())
}

# ── Years ────────────────────────────────────────────────────────────────────
# The requested years are the value of years_argument on its own field,
# which the mapper coerces to a list. A rule that checks years but names no
# argument finds none on any field, so it fails closed.

_years_apply(spec, ctx) if bound_here(spec.years_argument, ctx)

_years_apply(spec, ctx) if not spec.years_argument

_requested_years(spec, ctx) := {y | some y in argument(spec.years_argument, ctx)}

# The consent's granted_scopes together with the rule's own allowed_scopes.
_available_scopes(spec, ctx) := scopes if {
	consent_scopes := object.get(object.get(ctx.pip, "consent", {}), "granted_scopes", [])
	rule_scopes := object.get(spec, "allowed_scopes", [])
	scopes := {s | some s in consent_scopes} | {s | some s in rule_scopes}
}

# ── Arguments ────────────────────────────────────────────────────────────────
# A check that reads an argument is bound to one field ("ParentType.field")
# and argument, and reads that field's own value: each selection of the field
# is judged on its own arguments. On other fields the check does not apply.

field_key(field) := sprintf("%s.%s", [field.parentType, field.field])

bound_here(binding, ctx) if field_key(ctx.field) == binding.field

# The value as the request supplied it. Undefined when absent or (partly)
# from a schema default: the source applies its own default, which need not
# match the bundled schema, so a check that needs the value fails.
argument(binding, ctx) := entry.value if {
	entry := ctx.field.args[binding.arg]
	entry.origin != "schema-default"
	not entry.schemaDefaults
}

# ── Validity window ──────────────────────────────────────────────────────────

default within_validity_window(_) := false

within_validity_window(ctx) if {
	now_ns := _now_ns(ctx)
	end_ns := time.parse_rfc3339_ns(ctx.pip.consent.valid_until)
	now_ns < end_ns
}

_now_ns(ctx) := time.parse_rfc3339_ns(ctx.time) if ctx.time != ""

_now_ns(ctx) := time.now_ns() if {
	not ctx.time
} else := time.now_ns() if ctx.time == ""

_check_consent_context_valid(spec, ctx) := _axis(
	{"code": "CONSENT_CONTEXT_INVALID", "fail_code": _context_invalid_code(ctx), "label": "Signed consent context valid", "expected": "signature, issuer, audience and time claims valid"},
	_flag(spec, "consent_context_required"),
	_consent(ctx, "context_valid", false) == true,
)

# A forged, expired or unverifiable token denies with the consent PIP's own
# code for it; without one, e.g. no consent at all, the generic code stands.
_context_invalid_code(ctx) := code if {
	code := ctx.pip.consent.invalid_code
	is_string(code)
	code != ""
} else := "CONSENT_CONTEXT_INVALID"

_check_consent_status_available(spec, ctx) := _axis(
	{"code": "CONSENT_STATUS_UNAVAILABLE", "label": "Consent status available", "expected": "online consent-register status check succeeded"},
	_flag(spec, "consent_status_required"),
	_consent(ctx, "status_available", false) == true,
)
