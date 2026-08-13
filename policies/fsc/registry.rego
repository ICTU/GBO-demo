package fsc.registry

# Party registry for FSC contract acceptance (DvTP onboarding).
#
# This is the PIP behind doelbinding.auto_sign_contract: a party may only
# obtain a service-connection contract with one of our sources once it is
# registered here and active. Onboarding a new private party is therefore
# a registry edit, not a config change on the manager.
#
# Why Rego and not registry.json: the OpenFTV PAP loads EVERY file under
# PDP_POLICIES_STORE as a policy in the configured language
# (eam/pap/loader.go, loadPolicy) — a stray .json aborts the whole walk.
# The data is kept flat and JSON-shaped so it can move to an HTTP PIP
# later without touching the rules: that becomes a request-mapper that
# puts the same object under input.context.pip.registry.
#
# Keys are the 20-digit OIN carried in the peer's FSC certificate
# (serialNumber), i.e. the value that appears in a contract's peer_ids.

parties := {
	"99999999900000000100": {
		"name": "Demo EUDI-issuance (GBO)",
		"active": true,
		"sectors": ["eudi-issuer"],
		"register": "eIDAS art. 5a (mock)",
	},
	"99999999900000000300": {
		"name": "Demo Hypotheekverlener BV",
		"active": true,
		"sectors": ["hypotheekverlener"],
		"register": "KvK",
	},
	# Registered but suspended — kept in the demo set so the deny path
	# (PARTY_NOT_ACTIVE) is reachable without editing the file.
	"99999999900000000500": {
		"name": "Demo Incassobureau BV",
		"active": false,
		"sectors": ["incasso"],
		"register": "KvK",
	},
}

# known is true for a party that appears in the registry at all.
known(oin) if {
	parties[oin]
}

# active is true only for a known party whose entry is not suspended.
# A missing `active` field counts as suspended (fail closed).
active(oin) if {
	parties[oin].active == true
}
