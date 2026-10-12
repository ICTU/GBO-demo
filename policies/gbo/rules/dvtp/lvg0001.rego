package gbo.rules.dvtp.lvg0001

# LVG0001 — Ownership check of a verblijfsobject under a citizen's consent.
#
# The citizen consents in MijnOverheid that the Installatie Register may ask
# LVG whether a VBO is theirs; LVG answers with that VBO-id or null. The
# register's own consent for the installer is not a GBO consent and is not
# checked here.

import data.gbo.lib
import data.gbo.rules.dvtp.helper

rule_id := "LVG0001"

# Bound only when a consent token comes along; without one the EUDI rules
# judge the request. This decides whose reasons are reported, not access:
# without a verified token the consent checks below deny anyway.
regime := "consent"

covers_types := set()

covers_fields := {
	# the root field, which names the subject and the VBO-id
	"Query.vbo",
	# the only answer: the requested VBO-id, when the citizen owns it
	"Verblijfsobject.vboId",
}

# ── The consent ──────────────────────────────────────────────────────────────

deny contains helper.consent_invalid_reason if not helper.consent_verified

deny contains "CONSENT_STATUS_UNAVAILABLE" if not helper.consent_status_known

deny contains "CONSENT_NOT_FOUND" if not helper.consent_exists

deny contains "CONSENT_WITHDRAWN" if not helper.consent_active

deny contains "CONSENT_EXPIRED" if helper.consent_expired

deny contains "CONSENT_SCOPE_MISMATCH" if not helper.consent_covers_declared_scope

deny contains "CONSENT_ACTOR_MISMATCH" if not helper.consent_given_to_caller

# Only the ownership question. The consent check alone would not do: a token
# for bd:ib:2025, sent with that scope, covers it.
deny contains "SCOPE_NOT_ALLOWED" if lib.declared_scope != "lvg:vbo:eigendom"

# ── The question ─────────────────────────────────────────────────────────────

# Only the identity placeholder: the register can ask only about the
# consent's citizen, and LVG's API takes a BSN.
deny_field[i] contains "CONSTRAINT_MISMATCH" if {
	some i, f in lib.fields
	lib.key(f) == "Query.vbo"
	not helper.names_consent_citizen(f, "bsn", {lib.identity_placeholder})
}
