package doelbinding.auto_sign_contract_test

import data.doelbinding.auto_sign_contract as autosign

# The Belastingdienst-mock peer is the bronhouder whose Manager asks.
bd := "99999999900000000200"

brp := "99999999900000000400"

hv := "99999999900000000300"

edi := "99999999900000000100"

suspended := "99999999900000000500"

unknown_party := "00000009876543210000"

unknown_source := "00000000000000000001"

participants := {
	hv: {
		"name": "Demo Hypotheekverlener BV",
		"active": true,
		"allowed_source_oins": [bd],
	},
	suspended: {
		"name": "Demo Incassobureau BV",
		"active": false,
		"allowed_source_oins": [bd],
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

test_system_issuer_allowed_without_pip_data if {
	autosign.allow with input as connection(edi)
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
	resp.context.granted[0].source_oin == bd
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

test_delegated_service_connection_denied if {
	not is_allowed(contract_input(sort([bd, hv]), [3]))
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
