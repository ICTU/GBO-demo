package gbo.rules.eudi.helper

# ═══════════════════════════════════════════════════════════════════════════
# What the EUDI rules share: who may issue, and the PID regime.
#
# PID regime: no consent token, and the citizen named by the BSN the EUDI
# adapter took from the wallet's PID disclosure. The PID itself is not
# verified here; the adapter trusts the disclosure.
# ═══════════════════════════════════════════════════════════════════════════

import data.gbo.fsc_peers
import data.gbo.lib

# The designated issuers (as in eIDAS art. 5a designation). The FSC grant
# admits a peer to the service; this admits it to the EUDI rules. The list
# says who may issue attestations at all, not which source they read.
#
# The EDI issuer's FSC peer comes from fsc-infra/peers.txt. The other two are
# not parties of the local FSC network: the legal OIN of the demo issuer
# (services/dev-portal-backend/organizations.json) and the issuer in the
# simulation.
designated_issuers := {
	"99999999900000001700",
	"0000009961MINEZK0000",
	fsc_peers.peer_ids["edi-issuer"],
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
