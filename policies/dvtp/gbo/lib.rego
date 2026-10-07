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
#     "subject":  { ...AuthZEN subject: id = the FSC peer that connects,
#                   attributes.outway_delegator_peer_id = the peer it
#                   connects for, on a delegated connection },
#     "time":     "<RFC3339>",
#     "resource": { "scope": "...",
#                   "subject_placeholders": {<placeholders that stand for
#                                            the subject of the consent>} },
#     "pip":      { "consent": { "context_valid": bool, "exists": bool,
#                                "status_available": bool, "withdrawn": bool,
#                                "valid_until": "<RFC3339>",
#                                "granted_scopes": [...],
#                                "dienstverlener_oin": "...",
#                                "invalid_code": "<code>" },
#                 "integrator": { "active": bool,
#                                 "acts_for": [{"peer_id", "rules"}] } },
#                 (consent resolved by data.dvtp.gbo.consent; invalid_code
#                  only when context_valid is false. integrator is the
#                  acting peer's admission entry, on a delegated call only)
#     "field":    { the mapper's record of the field under evaluation:
#                   path, parentType, field, leaf, args (FTV GraphQL
#                   profile, Section 6.2) }
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

# What a consent-based query names its subject with. The consumer holds no
# identifier of the citizen: the consent token carries encrypted values per
# party, and the source puts its own decrypted value in this place after an
# allow. The placeholder says which form the source's API takes:
# identity_placeholder the BSN, pseudonym_placeholder the source's own
# pseudonym of the citizen. A rule lists the ones its API accepts in its
# constraint_binding. A query that names anything else is asking about someone
# the consent does not vouch for, or in a form the API does not take.
identity_placeholder := "consent:identity"

pseudonym_placeholder := "consent:pseudonym"

subject_placeholders := {identity_placeholder, pseudonym_placeholder}

evaluate(spec, ctx) := _outcome(spec, _short_circuit(_raw_steps(spec, ctx)))

# The steps are computed once per evaluation and passed on: a function
# result is not cached, so recomputing them per branch doubles the work.
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

# ── Cascade: raw step-list + short-circuit modifier ──────────────────────────
# All checks are first evaluated independently (pass/fail). Then we mark all
# checks AFTER the first fail as "skipped" — this fits the semantics that a
# DENY truncates the cascade, and the iWlz rendering that shows "skipped" as
# a separate status.

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
	# All constraint-bindings on this field must be satisfied (AND). For
	# multi-binding we report the FIRST unsatisfied binding as the
	# expected-string so that the error message stays specific. For
	# ALL-pass we show the number of bindings that were verified.
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

# PID regime: the request carries no consent token, and it names a subject.
# The EUDI adapter sends the BSN from the wallet's PID disclosure as it is,
# in the argument the rule names in `subject_argument` (the root field's
# `bsn`). In V1 nothing verifies the PID itself — the adapter trusts the
# disclosure — so this axis asserts only that the request is about someone
# and is not a consent request.
#
# The consent half keeps the regimes apart. pip.consent is present exactly
# when the request carried a consent token, verified or not, and both
# regimes name a subject, so without it this axis would pass on every
# consent-based request as well. It holds on every field. The subject half
# is checked on the field that carries the argument; the other fields of
# the request depend on that field, which the AND across fields requires.
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

# Scope-authorization: only active when the rule explicitly declares an
# allowed_scopes set. Without this axis a rule would only be scoped via
# covers_fields (implicitly through the engine's closed-world), and any
# arbitrary resource.scope-string could pass through — the authoritative
# source of scope per policy-path would be missing. DvTP covers this via
# consent-scope-cover; EUDI via a rule-declared whitelist.
_check_scope_allowed(spec, ctx) := _axis(
	{"code": "SCOPE_NOT_ALLOWED", "label": "Scope allowed for rule", "expected": sprintf("%q in spec.allowed_scopes", [_scope(ctx)]), "skipped": "no scope-whitelist configured"},
	count(object.get(spec, "allowed_scopes", set())) > 0,
	_scope(ctx) in object.get(spec, "allowed_scopes", set()),
)

# Direct year authorization for source-owned EUDI queries. Unlike the DvTP
# consent path this checks the actual GraphQL selector against a rule-owned
# set; it does not manufacture or trust a GBO catalog scope.
_check_years_allowed(spec, ctx) := _years_step(
	{"code": "YEAR_NOT_ALLOWED", "label": "Requested years allowed for rule", "passed": "allowed", "missing": "%v in spec.allowed_years", "skipped": "no year-whitelist configured"},
	count(object.get(spec, "allowed_years", set())) > 0,
	spec, ctx,
	[y | some y in _requested_years(spec, ctx); not sprintf("%v", [y]) in {sprintf("%v", [a]) | some a in object.get(spec, "allowed_years", set())}],
)

# Year-coverage: only active when the rule sets years_in_scopes. Every
# belastingjaar requested in the query (the argument the rule names in
# years_argument) must be covered by a scope of the form bd:ib:<year> in the available
# scopes — the consent's granted_scopes (DvTP) or the rule's
# allowed_scopes (EUDI). The BD bron-schema returns ALL aangiften for a
# person, so per-year authorization is only enforceable when the year
# selector travels inside the query; a missing filter therefore fails
# closed.
_check_years_in_scopes(spec, ctx) := _years_step(
	{"code": "YEAR_NOT_COVERED", "label": "Requested years covered by scopes", "passed": "covered", "missing": "bd:ib:%v in scopes", "skipped": "n/a"},
	_flag(spec, "years_in_scopes"),
	spec, ctx,
	[y | some y in _requested_years(spec, ctx); not sprintf("bd:ib:%v", [y]) in _available_scopes(spec, ctx)],
)

# Actor-authorization: only explicitly designated actors may trigger this
# rule. Supports e.g. eIDAS art. 5a-style designation: one designated
# EDI-issuer per attestation-type. Without this axis, any OIN that passes
# the FSC-transport (grant + inway) could trigger the rule; with this
# axis there is a separate policy-check that the actor is also allowed
# at rule-level.
_check_actor_allowed(spec, ctx) := _axis(
	{"code": "ACTOR_NOT_ALLOWED", "label": "Actor allowed for rule", "expected": sprintf("%q in spec.allowed_actors", [acting_party(ctx)]), "skipped": "no actor-whitelist configured"},
	count(object.get(spec, "allowed_actors", set())) > 0,
	acting_party(ctx) in object.get(spec, "allowed_actors", set()),
)

# ── Acting and represented party (FSC delegation) ──────────────────────────
# A service provider calls a source itself, or through an integrator: a
# processor that connects on its behalf under a DelegatedServiceConnection
# grant naming the service provider as delegator. The source's Manager then
# issues the access token to the integrator with the service provider in its
# `act` claim, and the Inway hands both to the PDP: subject.id is the peer
# that connects, subject.attributes.outway_delegator_peer_id the peer it
# connects for (open-fsc common/authzen AuthZenSubject). The token is signed
# by the source's own Manager from a contract all three parties signed, so
# the delegator is as trustworthy as subject.id.
#
# The decision uses both. Consent binds the represented party: the service
# provider the citizen consented to. The acting party needs authority of its
# own for that provider and this rule: consent does not give it any.

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

# Every delegated call needs a mandate, whatever the rule: an integrator is
# admitted per service provider and per rule, and a peer without one gets
# nothing from connecting on someone's behalf. A direct call has no
# integrator to check.
_check_integrator_mandate(spec, ctx) := _axis(
	{"code": "INTEGRATOR_NOT_REGISTERED", "label": _mandate_label, "expected": _mandate_expected(spec, ctx), "skipped": "n/a (direct call)"},
	delegated(ctx),
	integrator_mandated(spec, ctx),
)

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

# A step from a check: skipped when the rule does not declare it (active is
# false), otherwise pass or fail on whether it holds. Both arguments are
# true or false, never undefined: an undefined argument would drop the step.
_axis(check, active, holds) := _step(check.code, check.label, check.expected, "pass") if {
	active
	holds
} else := _step(object.get(check, "fail_code", check.code), check.label, check.expected, "fail") if {
	active
} else := _step_skipped(check.code, check.label, object.get(check, "skipped", "n/a"))

# The year checks have a third outcome: no years in the query at all, which
# fails closed (the source would return every year).
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

# ── Consent-scope-check ──────────────────────────────────────────────────────

default consent_covers_scope(_) := false

consent_covers_scope(ctx) if {
	some s in ctx.pip.consent.granted_scopes
	s == ctx.resource.scope
}

# ── Constraint-binding-check ─────────────────────────────────────────────────

# A binding names a field, its argument and the placeholders the rule's API
# accepts in it. The argument must be one of those, and a placeholder only
# counts while it stands for someone: without a verified consent the engine
# leaves resource.subject_placeholders empty, and nothing matches.
constraint_binding_satisfied(fm, ctx) if {
	value := argument(fm, ctx)
	value in fm.placeholders
	value in object.get(ctx.resource, "subject_placeholders", set())
}

# ── Year-coverage helpers ────────────────────────────────────────────────────
# The requested belastingjaren are the value of the argument the rule names
# in years_argument, on that field. The mapper coerced it to a list,
# whether the query wrote a literal list, a variable, or one year. A rule
# that checks years but names no argument applies everywhere and finds no
# years: it fails closed rather than check nothing.

_years_apply(spec, ctx) if bound_here(spec.years_argument, ctx)

_years_apply(spec, ctx) if not spec.years_argument

_requested_years(spec, ctx) := {y | some y in argument(spec.years_argument, ctx)}

# Scopes available to the flow: the consent's granted_scopes (DvTP) union
# the rule's own allowed_scopes (EUDI). Exactly one of the two is non-empty
# per flow.
_available_scopes(spec, ctx) := scopes if {
	consent_scopes := object.get(object.get(ctx.pip, "consent", {}), "granted_scopes", [])
	rule_scopes := object.get(spec, "allowed_scopes", [])
	scopes := {s | some s in consent_scopes} | {s | some s in rule_scopes}
}

# ── Arguments ────────────────────────────────────────────────────────────────
# A check that reads a query argument is bound to the field that carries it:
# `field` names the field key ("ParentType.field"), `arg` the argument. It
# applies to that field's own record only, with that record's own value, so
# two selections of the field are each judged on their own arguments (FTV
# GraphQL profile, Section 9.4). On every other field it does not apply.

field_key(field) := sprintf("%s.%s", [field.parentType, field.field])

bound_here(binding, ctx) if field_key(ctx.field) == binding.field

# The argument's value as the request supplied it. Undefined when the
# argument is absent, or when its value came from a schema default in whole
# or in part: the source executes its own default, which the bundled schema
# need not match. A check that needs the value then fails.
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

# Which check failed, when the consent PIP could tell: a forged, expired or
# unverifiable token each deny with a reason of their own. Without one — no
# consent at all — the generic code stands.
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
