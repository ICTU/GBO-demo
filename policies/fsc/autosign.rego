package doelbinding.auto_sign_contract

# Contract acceptance for an OpenFSC Manager (the bronhouder side).
#
# SCOPE — this package decides whether a *contract* submitted by a peer is
# counter-signed. It is deliberately separate from data.authz, which
# decides individual *data requests* (the DvTP/EUDI GraphQL traffic). The
# two share no rules, no reason codes and no input shape; a contract says
# "these parties may connect at all", a request says "this field may be
# read now". Do not import dvtp.gbo here.
#
# WHO CALLS THIS — the Manager itself, not an Inway. With
# --authzen-auto-sign-pdp-address set, ReceiveContractHandler posts an
# AuthZEN evaluation the moment a peer submits a contract, and signs only
# on decision=true (open-fsc manager/pkg/autosigner/pdp/authzen_auto_signer.go).
#
# PACKAGE NAME IS NOT FREE — the Manager hard-codes
# context.doelbinding = "auto_sign_contract", and OpenFTV derives the OPA
# entrypoint from it (eam/pdp/opa-embedded/auth.go, determinePath), so the
# decision is read from data.doelbinding.auto_sign_contract. Renaming this
# package silently disables the policy: the path then resolves to nothing
# and every contract stays pending.
#
# INPUT (AuthZEN PARC, after OpenFTV's entity mapping):
#
#   input.subject  = {type: "peer_id", id: <our own OIN>,
#                     attributes: {self_peer_id: <our OIN>,
#                                  peer_ids: [<every OIN on the contract>]}}
#   input.action   = {type: "name", id: "autosign_contract"}
#   input.resource = {type: "contract", id: <content hash>,
#                     attributes: {grant_types: [<int>, ...]}}
#   input.context  = {doelbinding: "auto_sign_contract", ...}
#
# The payload is all the Manager knows at this point: no service name, no
# outway thumbprint. Rules can therefore only judge WHO, not WHAT.
#
# OUTPUT — OpenFTV reads `allow` (bool) and, on deny, `reason` (string).
# The whole package document reaches the decision log, so `response`
# carries the detail needed to explain a refusal.

import data.fsc.registry

default allow := false

allow if response.decision

reason := response.context.reason_admin.code if {
	not response.decision
}

# --- grant types ------------------------------------------------------
#
# Integer codes from the Manager's domain (manager/domain/contract/grant_type.go):
#   1 servicePublication            3 delegatedServiceConnection
#   2 serviceConnection             4 delegatedServicePublication
#
# This replaces --auto-sign-grants=serviceConnection, which the Manager
# refuses to combine with a PDP address (manager/cmd/serve.go: both set is
# a fatal error). The gate therefore has to live here: without it the
# registry rule alone would also accept a delegated grant.

grant_type_service_connection := 2

# --- derived facts ----------------------------------------------------

_grant_types := {t | some t in input.resource.attributes.grant_types}

# Every party on the contract except ourselves. A plain service connection
# has exactly one; delegated grants carry more.
_counterparties := {p |
	some p in input.subject.attributes.peer_ids
	p != input.subject.attributes.self_peer_id
}

_unknown := {p |
	some p in _counterparties
	not registry.known(p)
}

_suspended := {p |
	some p in _counterparties
	registry.known(p)
	not registry.active(p)
}

# --- checks -----------------------------------------------------------
#
# Each check is a complete boolean rule (default false) so that a
# malformed input denies rather than leaving the check undefined.

default _well_formed := false

_well_formed if {
	input.action.id == "autosign_contract"
	input.resource.type == "contract"
	is_string(input.subject.attributes.self_peer_id)
	is_array(input.subject.attributes.peer_ids)
	is_array(input.resource.attributes.grant_types)
}

default _grant_types_allowed := false

_grant_types_allowed if {
	_grant_types == {grant_type_service_connection}
}

default _has_counterparty := false

_has_counterparty if {
	count(_counterparties) > 0
}

default _all_known := false

_all_known if {
	count(_unknown) == 0
}

default _all_active := false

_all_active if {
	count(_suspended) == 0
}

# Ordered cascade: the first failing check names the refusal.
_checks := [
	{"code": "NOT_AN_AUTOSIGN_REQUEST", "ok": _well_formed},
	{"code": "GRANT_TYPE_NOT_ALLOWED", "ok": _grant_types_allowed},
	{"code": "NO_COUNTERPARTY", "ok": _has_counterparty},
	{"code": "PARTY_NOT_IN_REGISTRY", "ok": _all_known},
	{"code": "PARTY_NOT_ACTIVE", "ok": _all_active},
]

_failed := [c |
	some c in _checks
	c.ok == false
]

# --- decision ---------------------------------------------------------

response := {
	"decision": false,
	"context": {
		"reason_admin": {"code": _failed[0].code},
		"counterparties": sort(_counterparties),
		"grant_types": sort(_grant_types),
	},
} if {
	count(_failed) > 0
}

else := {
	"decision": true,
	"context": {"granted": [{
		"rule": "FSC_AUTOSIGN_REGISTERED_PARTY",
		"counterparties": sort(_counterparties),
	}]},
}
