package doelbinding.auto_sign_contract

# Contract auto-signing for the OpenFSC Manager on the source holder's side.
# The Manager posts an AuthZEN evaluation when a peer submits a contract and
# counter-signs only on allow. This decides whether parties may connect at
# all, not what a request may read; it shares nothing with data.authz.
#
# The package name is fixed: the Manager sends doelbinding
# "auto_sign_contract" and OpenFTV derives the OPA path from it. Renamed, the
# policy resolves to nothing and every contract stays pending.
#
# Input: subject.attributes holds self_peer_id and peer_ids (every party on
# the contract), resource.attributes.grant_types the grant codes. The Manager
# sends no service name and no delegation direction, so this judges who
# connects, not to which service or in which role.
#
# Admitted parties come from the onboarding register, pulled by OpenFTV into
# data.entities.dvtp_participant. Each lists the sources it was admitted to,
# by Peer ID, and for an integrator the service providers it acts for.
#
# `reason` is the refusal code; `response` explains it in the decision log.

default allow := false

allow if response.decision

reason := response.context.reason_admin.code if {
	not response.decision
}

# --- grant types ------------------------------------------------------
#
# Codes from the OpenFSC Manager. Its --auto-sign-grants flag cannot be
# combined with a PDP address, so the grant gate lives here; without it the
# registry checks would also accept any delegated grant.

grant_type_service_connection := 2

grant_type_delegated_service_connection := 3

# --- derived facts ----------------------------------------------------

_subject_attributes := object.get(object.get(input, "subject", {}), "attributes", {})
_source_peer_id := object.get(_subject_attributes, "self_peer_id", "")
_pulled_participants := object.get(data.entities, "dvtp_participant", {}) if {
	data.entities
}

else := {}

_parties := _pulled_participants

_grant_types := {t | some t in input.resource.attributes.grant_types}

# Every party on the contract except ourselves.
_counterparties := {p |
	some p in input.subject.attributes.peer_ids
	p != input.subject.attributes.self_peer_id
}

_unknown := {p |
	some p in _counterparties
	not _parties[p]
}

_suspended := {p |
	some p in _counterparties
	_parties[p]
	object.get(_parties[p], "active", false) != true
}

_wrong_source := {p |
	some p in _counterparties
	_parties[p]
	object.get(_parties[p], "active", false) == true
	not _source_peer_id in object.get(_parties[p], "allowed_source_peer_ids", [])
}

# --- checks -----------------------------------------------------------
#
# Each check defaults to false, so malformed input denies rather than
# leaving a check undefined.

default _well_formed := false

_well_formed if {
	input.action.id == "autosign_contract"
	input.resource.type == "contract"
	is_string(input.subject.attributes.self_peer_id)
	input.subject.attributes.self_peer_id != ""
	is_array(input.subject.attributes.peer_ids)
	is_array(input.resource.attributes.grant_types)
}

default _grant_types_allowed := false

_grant_types_allowed if {
	_grant_types == {grant_type_service_connection}
}

_grant_types_allowed if {
	_delegated
}

default _delegated := false

_delegated if {
	_grant_types == {grant_type_delegated_service_connection}
}

# A delegated connection names the integrator, the service provider it acts
# for, and us. Both counterparties must pass the checks above, and one must
# be registered as acting for the other. The input does not say which is the
# delegator, so this admits the pair, not the direction. That is safe: the
# request policy checks the mandate on every call, so a contract signed the
# wrong way round is admitted here and denied there.
default _delegation_registered := true

_delegation_registered := false if {
	_delegated
	not _registered_pair
}

_registered_pair if {
	count(_counterparties) == 2
	some integrator in _counterparties
	some represented in _counterparties
	integrator != represented
	some mandate in object.get(_parties[integrator], "acts_for", [])
	mandate.peer_id == represented
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

default _source_allowed := false

_source_allowed if {
	count(_wrong_source) == 0
}

# Ordered cascade: the first failing check names the refusal.
_checks := [
	{"code": "NOT_AN_AUTOSIGN_REQUEST", "ok": _well_formed},
	{"code": "GRANT_TYPE_NOT_ALLOWED", "ok": _grant_types_allowed},
	{"code": "NO_COUNTERPARTY", "ok": _has_counterparty},
	{"code": "PARTY_NOT_IN_REGISTRY", "ok": _all_known},
	{"code": "PARTY_NOT_ACTIVE", "ok": _all_active},
	{"code": "SOURCE_NOT_ALLOWED", "ok": _source_allowed},
	{"code": "DELEGATION_NOT_REGISTERED", "ok": _delegation_registered},
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
		"rule": "FSC_AUTOSIGN_ADMITTED_PARTY",
		"counterparties": sort(_counterparties),
		"source_peer_id": _source_peer_id,
	}]},
}
