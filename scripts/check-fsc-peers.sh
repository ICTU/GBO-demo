#!/usr/bin/env bash
# Fails when a copy of the FSC party table fsc-infra/peers.txt has drifted
# from it:
#
#   1. policies/gbo/fsc_peers.rego is not what scripts/gen-fsc-peers.sh
#      makes of peers.txt now (fix: make fsc-peers).
#   2. The DVTP onboarding register's demo configuration
#      (services/dvtp-onboarding-register/config/demo.json) names an active
#      participant, a source holder, an integrator or a service provider
#      whose peer ID is not in peers.txt. Such a party has no group
#      certificate, so it cannot reach the register over FSC. An inactive
#      participant may have no peer: the demo lists one to show a refusal.
#
# demo.json is checked, not generated: its names, roles and allowed sources
# are register data that peers.txt does not hold.
#
# Needs bash and jq. Run by make policy-check and by the rego job in CI.
set -euo pipefail
cd "$(dirname "$0")/.."

POLICY=policies/gbo/fsc_peers.rego
DEMO=services/dvtp-onboarding-register/config/demo.json
status=0

tmp="$(mktemp)"
trap 'rm -f "${tmp}" "${tmp}.tmp"' EXIT
./scripts/gen-fsc-peers.sh "${tmp}"
if ! diff -u "${POLICY}" "${tmp}" >&2; then
	echo "${POLICY} is stale: run make fsc-peers and commit the result" >&2
	status=1
fi

known="$(cd fsc-infra/pki && bash bootstrap-peer.sh --list | cut -f3 | jq -R . | jq -sc .)"
unknown="$(jq -r --argjson known "${known}" '
	[ .source_holders[]?.peer_id,
	  (.system_participants[]?, .seed_participants[]? | select(.active) | .peer_id, .allowed_source_peer_ids[]?),
	  (.integrations[]? | .integrator_peer_id, .service_provider_peer_id),
	  .service_providers[]?.peer_id ]
	| unique | map(select(. as $id | $known | index($id) | not)) | .[]
' "${DEMO}")"
if [[ -n "${unknown}" ]]; then
	echo "${DEMO} names peer IDs that are not in fsc-infra/peers.txt:" >&2
	echo "${unknown}" >&2
	status=1
fi

if [[ "${status}" == 0 ]]; then
	echo "-> ${POLICY} and ${DEMO} match fsc-infra/peers.txt"
fi
exit "${status}"
