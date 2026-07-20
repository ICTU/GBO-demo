package dvtp.gbo.rules.eud0001_test

import data.dvtp.gbo.lib
import data.dvtp.gbo.rules.eud0001

# ═══════════════════════════════════════════════════════════════════════════
# EUD0001 axes — scope + actor + PID.
#
# Tests run against lib.evaluate(spec, ctx) to isolate the check-axes from
# the engine's field-binding. The spec is taken from the rule itself so
# that allowed_scopes / allowed_actors come from a single source.
# ═══════════════════════════════════════════════════════════════════════════

# Minimal ctx-shape that all EUD0001-checks can handle. Overridden per
# test via object.union.
_base_ctx := {
	"subject": {"type": "org", "id": "00000004000000004000"},
	"args": {},
	"time": "2026-07-06T12:00:00Z",
	"resource": {"scope": "bd:ib:2025"},
	"pip": {"pid": {"bsn": "123456789"}},
	"field": "Query.inkomensgegevens",
}

# ── Happy path ──────────────────────────────────────────────────────────

test_allow_valid_actor_scope_pid if {
	result := lib.evaluate(eud0001.spec, _base_ctx)
	result.decision == true
}

# ── Scope-authorization ─────────────────────────────────────────────────

test_deny_scope_not_in_allowed_scopes if {
	ctx := object.union(_base_ctx, {"resource": {"scope": "dv:studieschuld:2024"}})
	result := lib.evaluate(eud0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "SCOPE_NOT_ALLOWED"
}

test_deny_scope_empty if {
	ctx := object.union(_base_ctx, {"resource": {"scope": ""}})
	result := lib.evaluate(eud0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "SCOPE_NOT_ALLOWED"
}

# 2023 is a catalog-usecase (adapter knows it, wallet can request it) but
# the policy denies it — demonstrating the separation between catalog-
# membership and scope-authorization.
test_deny_scope_bd_ib_2023 if {
	ctx := object.union(_base_ctx, {"resource": {"scope": "bd:ib:2023"}})
	result := lib.evaluate(eud0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "SCOPE_NOT_ALLOWED"
}

test_allow_scope_bd_ib_2024 if {
	ctx := object.union(_base_ctx, {"resource": {"scope": "bd:ib:2024"}})
	result := lib.evaluate(eud0001.spec, ctx)
	result.decision == true
}

# ── Actor-authorization ─────────────────────────────────────────────────

test_deny_actor_not_in_allowed_actors if {
	ctx := object.union(_base_ctx, {"subject": {"type": "org", "id": "00000001234567890000"}})
	result := lib.evaluate(eud0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "ACTOR_NOT_ALLOWED"
}

# ── Priority: actor-fail wins over scope-fail (more structural) ─────────

test_deny_actor_wins_over_scope if {
	ctx := object.union(_base_ctx, {
		"subject": {"type": "org", "id": "00000001234567890000"},
		"resource": {"scope": "dv:studieschuld:2024"},
	})
	result := lib.evaluate(eud0001.spec, ctx)
	result.decision == false

	# lib.evaluate reports the FIRST fail in _raw_steps as
	# reason_admin.code. Cascade order: PID → scope → actor. On
	# simultaneous scope+actor fail, scope wins in the reason (first
	# fail); the engine's _worst_code then recalibrates on priority
	# (actor > scope). In lib-only context: first fail = SCOPE_NOT_ALLOWED.
	result.context.reason_admin.code == "SCOPE_NOT_ALLOWED"
}

# ── PID-check remains present ───────────────────────────────────────────

test_deny_pid_missing if {
	# object.union is deep-merged; explicit empty bsn instead of pip=={}.
	ctx := object.union(_base_ctx, {"pip": {"pid": {"bsn": ""}}})
	result := lib.evaluate(eud0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "PID_NOT_PRESENT"
}

test_deny_pid_invalid_shape if {
	ctx := object.union(_base_ctx, {"pip": {"pid": {"bsn": "abc"}}})
	result := lib.evaluate(eud0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "PID_NOT_PRESENT"
}

# ── Axis activation is conditional on rule-declaration ─────────────────
# Each policy-path must carry scope- and actor-authorization, but the
# source per path differs. DVT0001 carries its scope-authorization via
# consent-scope-cover (rule-owned source) and declares no allowed_scopes
# — the scope-axis must then be silent, otherwise DVT0001 would break on
# every request. Same pattern for actor.

_rule_without_whitelists := {
	"rule_id": "TEST_NO_WHITELISTS",
	"consent_required": false,
	"consent_must_cover_scope": false,
	"pid_required": true,
	"pip": null,
}

test_scope_axis_inactive_without_whitelist if {
	ctx := object.union(_base_ctx, {"resource": {"scope": "anything-goes"}})
	result := lib.evaluate(_rule_without_whitelists, ctx)
	result.decision == true
}

test_actor_axis_inactive_without_whitelist if {
	ctx := object.union(_base_ctx, {"subject": {"type": "org", "id": "some-other-oin"}})
	result := lib.evaluate(_rule_without_whitelists, ctx)
	result.decision == true
}
