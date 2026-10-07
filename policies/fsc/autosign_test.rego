package doelbinding.auto_sign_contract_test

import data.doelbinding.auto_sign_contract as autosign

# The Belastingdienst-mock peer is the bronhouder whose Manager asks.
bd := "0000009958MINBZK0000"

brp := "0000009958RVIG000000"

hv := "0000009950HYPBV00000"

edi := "0000009961MINEZK0000"

suspended := "99999999900000000500"

unknown_party := "00000009876543210000"

unknown_source := "00000000000000000001"

participants := {
	hv: {
		"name": "Demo Hypotheekverlener BV",
		"active": true,
		"allowed_source_peer_ids": [bd],
	},
	suspended: {
		"name": "Demo Incassobureau BV",
		"active": false,
		"allowed_source_peer_ids": [bd],
	},
	edi: {
		"name": "Demo EUDI-issuance (GBO)",
		"active": true,
		"allowed_source_peer_ids": [bd, brp],
	},
}

# Mirrors the body a Manager posts, after OpenFTV's entity mapping.
contract_input_for(source, peer_ids, grant_types) := {
	"subject": {
		"type": "peer_id",
		"id": source,
		"attributes": {"self_peer_id": source, "peer_ids": peer_ids},
	},
	"action": {"type": "name", "id": "autosign_contract"},
	"resource": {
		"type": "contract",
		"id": "$1$1$abcdef",
		"attributes": {"grant_types": grant_types},
	},
	"context": {"doelbinding": "auto_sign_contract"},
}

contract_input(peer_ids, grant_types) := contract_input_for(bd, peer_ids, grant_types)

connection_for(source, consumer) := contract_input_for(source, sort([source, consumer]), [2])

connection(consumer) := contract_input(sort([bd, consumer]), [2])

is_allowed(request) if {
	autosign.allow with input as request with data.entities as {"dvtp_participant": participants}
}

deny_reason(request) := value if {
	value := autosign.reason with input as request with data.entities as {"dvtp_participant": participants}
}

decision_response(request) := value if {
	value := autosign.response with input as request with data.entities as {"dvtp_participant": participants}
}

# --- allow ------------------------------------------------------------

test_registered_consumer_allowed if {
	is_allowed(connection(hv))
}

test_registered_issuer_allowed if {
	is_allowed(connection(edi))
}

test_system_issuer_denied_without_pip_data if {
	not autosign.allow with input as connection(edi)
}

test_system_issuer_allowed_for_brp if {
	is_allowed(connection_for(brp, edi))
}

test_consumer_denied_for_source_without_admission if {
	not is_allowed(connection_for(brp, hv))
}

test_wrong_source_reason if {
	deny_reason(connection_for(brp, hv)) == "SOURCE_NOT_ALLOWED"
}

test_unknown_source_reason if {
	deny_reason(connection_for(unknown_source, hv)) == "SOURCE_NOT_ALLOWED"
}

test_allow_reports_counterparty if {
	resp := decision_response(connection(hv))
	resp.context.granted[0].rule == "FSC_AUTOSIGN_ADMITTED_PARTY"
	resp.context.granted[0].counterparties == [hv]
	resp.context.granted[0].source_peer_id == bd
}

# --- registry -------------------------------------------------------

test_unknown_party_denied if {
	not is_allowed(connection(unknown_party))
}

test_unknown_party_reason if {
	deny_reason(connection(unknown_party)) == "PARTY_NOT_IN_REGISTRY"
}

test_suspended_party_denied if {
	not is_allowed(connection(suspended))
}

test_suspended_party_reason if {
	deny_reason(connection(suspended)) == "PARTY_NOT_ACTIVE"
}

# One bad party among several is enough to refuse the whole contract.
test_mixed_parties_denied if {
	not is_allowed(contract_input(sort([bd, hv, unknown_party]), [2]))
}

# --- grant types ------------------------------------------------------

test_service_publication_denied if {
	not is_allowed(contract_input(sort([bd, hv]), [1]))
}

# --- delegated connections -------------------------------------------
# An integrator connects for a service provider it is registered to act for.

integrator := "0000009950INTEGR0000"

with_integrator := object.union(participants, {integrator: {
	"name": "Demo Integrator BV",
	"active": true,
	"allowed_source_peer_ids": [bd],
	"acts_for": [{"peer_id": hv, "rules": ["DVT0001"]}],
}})

delegated_for(peer_ids) := contract_input(sort(peer_ids), [3])

deny_reason_with(request, parties) := value if {
	value := autosign.reason with input as request with data.entities as {"dvtp_participant": parties}
}

test_delegated_connection_for_registered_provider_allowed if {
	autosign.allow with input as delegated_for([bd, hv, integrator])
		with data.entities as {"dvtp_participant": with_integrator}
}

test_delegated_connection_for_unregistered_provider_denied if {
	deny_reason_with(delegated_for([bd, edi, integrator]), with_integrator) == "DELEGATION_NOT_REGISTERED"
}

test_delegated_connection_between_unrelated_parties_denied if {
	deny_reason_with(delegated_for([bd, hv, edi]), with_integrator) == "DELEGATION_NOT_REGISTERED"
}

# The integrator must be admitted in its own right; a mandate is not enough.
test_delegated_connection_by_suspended_integrator_denied if {
	suspended_integrator := object.union(with_integrator, {integrator: object.union(with_integrator[integrator], {"active": false})})
	deny_reason_with(delegated_for([bd, hv, integrator]), suspended_integrator) == "PARTY_NOT_ACTIVE"
}

test_delegated_connection_by_unknown_integrator_denied if {
	deny_reason(delegated_for([bd, hv, integrator])) == "PARTY_NOT_IN_REGISTRY"
}

# A mandate does not stand in for the provider's own admission.
test_delegated_connection_for_suspended_provider_denied if {
	for_suspended := object.union(with_integrator, {integrator: object.union(with_integrator[integrator], {"acts_for": [{"peer_id": suspended, "rules": ["DVT0001"]}]})})
	deny_reason_with(delegated_for([bd, suspended, integrator]), for_suspended) == "PARTY_NOT_ACTIVE"
}

# The direction is not visible here, but a third counterparty is refused.
test_delegated_connection_with_extra_party_denied if {
	deny_reason_with(delegated_for([bd, hv, edi, integrator]), with_integrator) == "DELEGATION_NOT_REGISTERED"
}

test_delegated_service_publication_denied if {
	not is_allowed(contract_input(sort([bd, hv]), [4]))
}

test_mixed_grant_types_denied if {
	not is_allowed(contract_input(sort([bd, hv]), [2, 3]))
}

test_empty_grant_types_denied if {
	deny_reason(contract_input(sort([bd, hv]), [])) == "GRANT_TYPE_NOT_ALLOWED"
}

# --- malformed input --------------------------------------------------

test_contract_without_counterparty_denied if {
	deny_reason(contract_input([bd], [2])) == "NO_COUNTERPARTY"
}

test_other_action_denied if {
	deny_reason(object.union(
		connection(hv),
		{"action": {"type": "name", "id": "something_else"}},
	)) == "NOT_AN_AUTOSIGN_REQUEST"
}

test_missing_attributes_denied if {
	not is_allowed({
		"subject": {"type": "peer_id", "id": bd},
		"action": {"type": "name", "id": "autosign_contract"},
		"resource": {"type": "contract", "id": "$1$1$abcdef"},
		"context": {"doelbinding": "auto_sign_contract"},
	})
}

test_empty_input_denied if {
	not is_allowed({})
}
