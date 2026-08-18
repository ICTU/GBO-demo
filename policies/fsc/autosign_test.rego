package doelbinding.auto_sign_contract_test

import data.doelbinding.auto_sign_contract as autosign

# The Belastingdienst-mock peer is the bronhouder whose Manager asks.
bd := "99999999900000000200"

brp := "99999999900000000400"

hv := "99999999900000000300"

edi := "99999999900000000100"

suspended := "99999999900000000500"

unknown_party := "00000009876543210000"

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

# --- allow ------------------------------------------------------------

test_registered_consumer_allowed if {
	autosign.allow with input as connection(hv)
}

test_registered_issuer_allowed if {
	autosign.allow with input as connection(edi)
}

# The demo registry is deliberately shared by both source Managers. This
# provider-independent scope must be revisited for production (issue #230).
test_registered_consumer_allowed_for_other_source if {
	autosign.allow with input as connection_for(brp, hv)
}

test_allow_reports_counterparty if {
	resp := autosign.response with input as connection(hv)
	resp.context.granted[0].rule == "FSC_AUTOSIGN_REGISTERED_PARTY"
	resp.context.granted[0].counterparties == [hv]
}

# --- registry -------------------------------------------------------

test_unknown_party_denied if {
	not autosign.allow with input as connection(unknown_party)
}

test_unknown_party_reason if {
	autosign.reason == "PARTY_NOT_IN_REGISTRY" with input as connection(unknown_party)
}

test_suspended_party_denied if {
	not autosign.allow with input as connection(suspended)
}

test_suspended_party_reason if {
	autosign.reason == "PARTY_NOT_ACTIVE" with input as connection(suspended)
}

# One bad party among several is enough to refuse the whole contract.
test_mixed_parties_denied if {
	not autosign.allow with input as contract_input(sort([bd, hv, unknown_party]), [2])
}

# --- grant types ------------------------------------------------------

test_service_publication_denied if {
	not autosign.allow with input as contract_input(sort([bd, hv]), [1])
}

test_delegated_service_connection_denied if {
	not autosign.allow with input as contract_input(sort([bd, hv]), [3])
}

test_mixed_grant_types_denied if {
	not autosign.allow with input as contract_input(sort([bd, hv]), [2, 3])
}

test_empty_grant_types_denied if {
	autosign.reason == "GRANT_TYPE_NOT_ALLOWED" with input as contract_input(sort([bd, hv]), [])
}

# --- malformed input --------------------------------------------------

test_contract_without_counterparty_denied if {
	autosign.reason == "NO_COUNTERPARTY" with input as contract_input([bd], [2])
}

test_other_action_denied if {
	autosign.reason == "NOT_AN_AUTOSIGN_REQUEST" with input as object.union(
		connection(hv),
		{"action": {"type": "name", "id": "something_else"}},
	)
}

test_missing_attributes_denied if {
	not autosign.allow with input as {
		"subject": {"type": "peer_id", "id": bd},
		"action": {"type": "name", "id": "autosign_contract"},
		"resource": {"type": "contract", "id": "$1$1$abcdef"},
		"context": {"doelbinding": "auto_sign_contract"},
	}
}

test_empty_input_denied if {
	not autosign.allow with input as {}
}
