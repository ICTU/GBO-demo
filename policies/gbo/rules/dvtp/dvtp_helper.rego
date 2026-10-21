package gbo.rules.dvtp.helper

# ═══════════════════════════════════════════════════════════════════════════
# What the consent rules share: the conditions on the citizen's consent.
#
# The consent itself comes from the consent PIP (gbo/consent.rego), which
# verified the token and asked the register for its status. Whatever the
# request carries under context.pip is not read: the policy decides only on
# a consent it verified itself.
# ═══════════════════════════════════════════════════════════════════════════

import data.gbo.lib

consent := c if {
	c := data.gbo.consent.resolved
} else := {}

# Signature, issuer, audience and time claims of the token are valid.
default consent_verified := false

consent_verified if consent.context_valid == true

# Why the token did not verify, as the consent PIP named it (forged,
# expired, keys unreachable); without a reason, e.g. no token, the generic
# code.
consent_invalid_reason := code if {
	code := consent.invalid_code
	is_string(code)
	code != ""
} else := "CONSENT_CONTEXT_INVALID"

# The register answered the status lookup.
default consent_status_known := false

consent_status_known if consent.status_available == true

default consent_exists := false

consent_exists if consent.exists == true

# Withdrawn must be known to be false; a missing answer is no consent.
default consent_active := false

consent_active if consent.withdrawn == false

default consent_expired := true

consent_expired := false if lib.now_ns < time.parse_rfc3339_ns(consent.valid_until)

# The citizen consented to this scope. Always true or false, so a rule can
# negate it: `not x in consent.granted_scopes` would let a consent without
# granted_scopes through.
default consent_covers(_) := false

consent_covers(scope) if scope in consent.granted_scopes

# The consent names the party the call is made for: on a delegated call the
# service provider, not the integrator. Consent gives the integrator no
# mandate; the engine checks that separately.
default consent_given_to_caller := false

consent_given_to_caller if {
	lib.represented_party != ""
	lib.represented_party == consent.dienstverlener_oin
}

# A placeholder stands for the citizen only under a verified consent;
# without one it names nobody.
names_consent_citizen(f, name, placeholders) if {
	consent_verified
	value := lib.argument(f, name)
	value in placeholders
	value in lib.subject_placeholders
}
