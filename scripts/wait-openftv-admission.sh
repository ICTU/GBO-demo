#!/usr/bin/env bash
# Wait until OpenFTV has pulled the seeded DvTP admission data and the real
# autosign entrypoint allows Hypotheek-BV for the Belastingdienst source.

set -euo pipefail

pdp_port="${PORT_PDP:-9181}"
max_attempts="${PDP_ADMISSION_READY_ATTEMPTS:-30}"
pdp_url="https://openftv-pdp:${pdp_port}/authzen/v1/evaluation"

request='{
  "subject": {
    "type": "peer_id",
    "id": "99999999900000000200",
    "properties": {
      "self_peer_id": "99999999900000000200",
      "peer_ids": ["99999999900000000200", "99999999900000000300"]
    }
  },
  "action": {"name": "autosign_contract"},
  "resource": {
    "type": "contract",
    "id": "pdp-admission-readiness",
    "properties": {"grant_types": [2]}
  },
  "context": {"doelbinding": "auto_sign_contract"}
}'

last_response=""
for _ in $(seq 1 "$max_attempts"); do
  last_response=$(curl --silent --show-error --max-time 2 \
    --noproxy '*' \
    --resolve "openftv-pdp:${pdp_port}:127.0.0.1" \
    --cacert services/openftv-pdp/certs/pdp-service.pem \
    --header "Content-Type: application/json" \
    --data "$request" \
    "$pdp_url" 2>/dev/null || true)
  if printf '%s' "$last_response" | jq -e '.decision == true' >/dev/null 2>&1; then
    echo "  ✓ OpenFTV admission data ready"
    exit 0
  fi
  sleep 1
done

reason=$(printf '%s' "$last_response" | jq -r '.context.reasonUser.en // .title // "no response"' 2>/dev/null || echo "no response")
echo "OpenFTV did not load the seeded Hypotheek-BV admission within ${max_attempts}s (last result: $reason)" >&2
exit 1
