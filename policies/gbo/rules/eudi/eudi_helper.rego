package gbo.rules.eudi.helper

# ═══════════════════════════════════════════════════════════════════════════
# What the EUDI rules share: who may issue, and the PID regime.
#
# PID regime: no consent token, and the citizen named by the BSN the EUDI
# adapter took from the wallet's PID disclosure. The PID itself is not
# verified here; the adapter trusts the disclosure.
# ═══════════════════════════════════════════════════════════════════════════

import data.gbo.lib

# The designated issuers (as in eIDAS art. 5a designation). The FSC grant
# admits a peer to the service; this admits it to the EUDI rules. The list
# says who may issue attestations at all, not which source they read.
designated_issuers := {
	"00000004000000004000",
	"0000009961MINEZK0000",
	"99999999900000000100",
}

default issuer_designated := false

issuer_designated if lib.acting_party in designated_issuers

# Any consent token, verified or not, puts the request outside the PID
# regime: both regimes name a subject, and only the token tells them apart.
default consent_token_presented := false

consent_token_presented if data.gbo.consent.resolved

# The argument names a citizen: a BSN, not empty and not a placeholder. A
# placeholder stands for the subject of a consent, and here there is none.
names_a_citizen(f, name) if {
	value := lib.argument(f, name)
	is_string(value)
	value != ""
	not value in lib.subject_placeholders
}
