package dvtp.gbo.rules.dvt0001_test

import data.dvtp.gbo.lib
import data.dvtp.gbo.rules.dvt0001

# ═══════════════════════════════════════════════════════════════════════════
# DVT0001 axes — consent + constraint-binding.
#
# Tests run against lib.evaluate(spec, ctx) to isolate the check-axes from
# the engine's field-binding. The spec is taken from the rule itself.
# ═══════════════════════════════════════════════════════════════════════════

# Minimal ctx-shape that carries a valid consent, a matching PI-binding,
# and the query-argument that the constraint-binding checks. Overridden
# per test via object.union.
_base_ctx := {
	"subject": {"type": "org", "id": "99999999900000000300"},
	"args": {"input.burgerservicenummer": "PI-abc123"},
	"time": "2026-07-06T12:00:00Z",
	"resource": {
		"scope": "bd:ib:2025",
		"pi": "PI-abc123",
	},
	"pip": {"consent": {
		"exists": true,
		"withdrawn": false,
		"valid_until": "2030-01-01T00:00:00Z",
		"granted_scopes": ["bd:ib:2025"],
		"pi": "PI-abc123",
	}},
	"field": "Query.inkomensgegevens",
}

# ── Happy path ──────────────────────────────────────────────────────────

test_allow_valid_consent_and_binding if {
	result := lib.evaluate(dvt0001.spec, _base_ctx)
	result.decision == true
}

# ── Consent-existence ───────────────────────────────────────────────────

test_deny_consent_not_found if {
	ctx := object.union(_base_ctx, {"pip": {"consent": {"exists": false}}})
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "CONSENT_NOT_FOUND"
}

# ── Consent-status ──────────────────────────────────────────────────────

test_deny_consent_withdrawn if {
	ctx := object.union(_base_ctx, {"pip": {"consent": object.union(_base_ctx.pip.consent, {"withdrawn": true})}})
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "CONSENT_WITHDRAWN"
}

test_deny_consent_expired if {
	ctx := object.union(_base_ctx, {"pip": {"consent": object.union(_base_ctx.pip.consent, {"valid_until": "2020-01-01T00:00:00Z"})}})
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "CONSENT_EXPIRED"
}

# ── Scope-membership in consent ─────────────────────────────────────────

test_deny_scope_not_in_granted_scopes if {
	ctx := object.union(_base_ctx, {
		"resource": object.union(_base_ctx.resource, {"scope": "bd:ib:2024"}),
		"pip": {"consent": object.union(_base_ctx.pip.consent, {"granted_scopes": ["bd:ib:2025"]})},
	})
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "CONSENT_SCOPE_MISMATCH"
}

# ── Constraint-binding (PI in query-arg must equal resource.pi) ─────────

test_deny_constraint_mismatch if {
	ctx := object.union(_base_ctx, {
		"args": {"input.burgerservicenummer": "PI-different"},
	})
	result := lib.evaluate(dvt0001.spec, ctx)
	result.decision == false
	result.context.reason_admin.code == "CONSTRAINT_MISMATCH"
}
