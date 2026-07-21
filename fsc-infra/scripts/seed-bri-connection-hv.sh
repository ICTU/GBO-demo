#!/usr/bin/env bash
# Connection contract HV -> BD for the bri-service with DvTP flow
# properties. The publication contract (BD -> Directory) is unchanged —
# one publication per service, multiple connections (per consumer).
#
# Grant properties:
#   flow: dvtp:query          (policy dispatch)
#   subject_id_type: pseudonym (sidecar substitutes PI -> BSN)
#
# Idempotent: skipped if the connection is Valid and the grant-link is
# set.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FSC_INFRA_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

GROUP_ID="${GROUP_ID:-fsc-demo}"
SERVICE_NAME="${SERVICE_NAME:-bri}"

HV_PEER_ID="${HV_PEER_ID:-99999999900000000300}"
BD_PEER_ID="${BD_PEER_ID:-99999999900000000200}"

HV_MANAGER_URL="${HV_MANAGER_URL:-https://hv-manager:9443}"

HV_INTERNAL_DIR="$FSC_INFRA_DIR/orgs/hypotheekverlener-mock/pki/internal"
HV_ORG_CERT="$FSC_INFRA_DIR/orgs/hypotheekverlener-mock/pki/org/hypotheekverlener.pem"

HV_CERT="$HV_INTERNAL_DIR/internal-cert.pem"
HV_KEY="$HV_INTERNAL_DIR/internal-cert-key.pem"
HV_CA="$HV_INTERNAL_DIR/intermediate_ca.pem"

GRANT_LINK_PATH="${GRANT_LINK_PATH:-/bri}"
OUTWAY_NAME="${OUTWAY_NAME:-HvOutway-01}"
PG_HOST="${PG_HOST:-postgres}"
PG_USER="${PG_USER:-postgres}"
export PGPASSWORD="${PGPASSWORD:-${FSC_POSTGRES_PASSWORD}}"

for f in "$HV_CERT" "$HV_KEY" "$HV_CA" "$HV_ORG_CERT"; do
  if [ ! -f "$f" ]; then
    echo "missing cert file: $f" >&2
    echo "run 'make fsc-hv-certs' first" >&2
    exit 1
  fi
done

# ── 1. Connection contract in hv-Manager ──────────────────────────────

echo "[1/2] Create connection contract HV → BD for '$SERVICE_NAME'..."

existing=$(mtls_curl "$HV_CERT" "$HV_KEY" "$HV_CA" \
  "${HV_MANAGER_URL}/v1/contracts?grant_type=GRANT_TYPE_SERVICE_CONNECTION&service_name=${SERVICE_NAME}" \
  || echo '{}')

if echo "$existing" | jq -e '.contracts[]? | select(.state == "CONTRACT_STATE_VALID")' >/dev/null 2>&1; then
  echo "  → connection contract already Valid, skipping create"
else
  outway_thumbprint=$(pubkey_thumbprint_hex "$HV_ORG_CERT")
  if [ -z "$outway_thumbprint" ]; then
    echo "  ✗ could not compute outway pubkey thumbprint"
    exit 1
  fi
  conn_body=$(jq -n \
    --arg iv "$(uuid_v7)" \
    --arg group "$GROUP_ID" \
    --argjson not_before "$(now_epoch)" \
    --argjson not_after "$(plus_years_epoch 5)" \
    --argjson created_at "$(now_epoch)" \
    --arg svc_name "$SERVICE_NAME" \
    --arg bd_peer "$BD_PEER_ID" \
    --arg hv_peer "$HV_PEER_ID" \
    --arg thumb "$outway_thumbprint" \
    '{
      contract_content: {
        iv: $iv,
        group_id: $group,
        validity: {not_before: $not_before, not_after: $not_after},
        hash_algorithm: "HASH_ALGORITHM_SHA3_512",
        created_at: $created_at,
        grants: [{
          type: "GRANT_TYPE_SERVICE_CONNECTION",
          outway: {
            peer_id: $hv_peer,
            identification: {
              type: "OUTWAY_IDENTIFICATION_TYPE_PUBLIC_KEY_THUMBPRINT",
              public_key_thumbprint: $thumb
            }
          },
          service: {type: "SERVICE_TYPE_SERVICE", peer_id: $bd_peer, name: $svc_name},
          properties: {
            "flow": "dvtp:query",
            "subject_id_type": "pseudonym"
          }
        }]
      }
    }')
  http_status=$(mtls_curl "$HV_CERT" "$HV_KEY" "$HV_CA" \
    "$HV_MANAGER_URL/v1/contracts" \
    -X POST -H "Content-Type: application/json" \
    -d "$conn_body" -o /tmp/hv-conn-out.txt -w "%{http_code}")
  if [ "$http_status" != "201" ]; then
    echo "  ✗ connection contract creation failed (HTTP $http_status)"
    cat /tmp/hv-conn-out.txt
    exit 1
  fi
  echo "  ✓ connection contract created; waiting for auto-sign..."
  for _ in $(seq 30); do
    sleep 1
    st=$(mtls_curl "$HV_CERT" "$HV_KEY" "$HV_CA" \
      "$HV_MANAGER_URL/v1/contracts?grant_type=GRANT_TYPE_SERVICE_CONNECTION&service_name=$SERVICE_NAME" \
      | jq -r 'first(.contracts[]?.state)' 2>/dev/null || echo "")
    if [ "$st" = "CONTRACT_STATE_VALID" ]; then
      echo "  ✓ connection contract Valid"
      break
    fi
  done
fi

# ── 2. Grant-link upsert in hv_controller ─────────────────────────────

echo "[2/2] Upsert grant-link '$GRANT_LINK_PATH' → HV connection grant-hash..."
new_hash=$(mtls_curl "$HV_CERT" "$HV_KEY" "$HV_CA" \
  "${HV_MANAGER_URL}/v1/contracts?grant_type=GRANT_TYPE_SERVICE_CONNECTION&service_name=${SERVICE_NAME}&limit=1" \
  | jq -r '.contracts[0].content.grants[0].hash // empty')
if [ -z "$new_hash" ]; then
  echo "  x no HV connection grant-hash found — abort"
  exit 1
fi
psql -h "$PG_HOST" -U "$PG_USER" -d fsc_hv_controller -c "
  INSERT INTO controller.outway_grant_links (outway_name, url_path, grant_hash, outway_group_id)
  VALUES ('$OUTWAY_NAME', '$GRANT_LINK_PATH', '$new_hash', '$GROUP_ID')
  ON CONFLICT (outway_group_id, outway_name, url_path)
  DO UPDATE SET grant_hash = EXCLUDED.grant_hash
" > /dev/null
echo "  ✓ grant-link $OUTWAY_NAME $GRANT_LINK_PATH → ${new_hash:0:22}..."

echo ""
echo "HV -> BD contract-seed done. Dienstverlener-backend can POST"
echo "to http://hv-outway:8080/bri (subject_id_type=pseudonym -> sidecar"
echo "substitutes PI -> BSN)."
