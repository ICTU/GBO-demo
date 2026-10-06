#!/usr/bin/env bash
# Delegated connection: the integrator (...1100) connects to BD's bri
# service on behalf of Hypotheek-BV (...0300).
#
# One contract, one DelegatedServiceConnectionGrant, three signatures:
#   integrator  — delegatee; its Outway connects. Signs by creating it.
#   HV          — delegator; the service provider the integrator acts for.
#                 HV's Manager has no autosign, so this script accepts on
#                 HV's behalf, as HV's operator would in its Controller UI.
#   BD          — the source. Its Manager puts the contract to the PDP
#                 (policies/fsc/autosign.rego), which admits it only when
#                 the integrator is registered as acting for HV in the DvTP
#                 onboarding register.
#
# BD's Manager then issues the integrator's access tokens with HV in the
# `act` claim, and the Inway passes it to the PDP as
# subject.attributes.outway_delegator_peer_id.
#
# The publication contract (BD -> Directory) is the one fsc-seed-bri made.
#
# Idempotent: skipped if the delegated connection is Valid; the grant-link
# is upserted every run.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FSC_INFRA_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

GROUP_ID="${GROUP_ID:-fsc-demo}"
SERVICE_NAME="${SERVICE_NAME:-bri}"

INT_PEER_ID="${INT_PEER_ID:-99999999900000001100}"
HV_PEER_ID="${HV_PEER_ID:-99999999900000000300}"
BD_PEER_ID="${BD_PEER_ID:-99999999900000000200}"

INT_MANAGER_URL="${INT_MANAGER_URL:-https://int-manager:9443}"
HV_MANAGER_URL="${HV_MANAGER_URL:-https://hv-manager:9443}"

INT_INTERNAL_DIR="$FSC_INFRA_DIR/orgs/integrator/pki/internal"
INT_ORG_CERT="$FSC_INFRA_DIR/orgs/integrator/pki/org/integrator.pem"
HV_INTERNAL_DIR="$FSC_INFRA_DIR/orgs/hypotheekverlener-mock/pki/internal"

INT_CERT="$INT_INTERNAL_DIR/internal-cert.pem"
INT_KEY="$INT_INTERNAL_DIR/internal-cert-key.pem"
INT_CA="$INT_INTERNAL_DIR/intermediate_ca.pem"
HV_CERT="$HV_INTERNAL_DIR/internal-cert.pem"
HV_KEY="$HV_INTERNAL_DIR/internal-cert-key.pem"
HV_CA="$HV_INTERNAL_DIR/intermediate_ca.pem"

# The same grant properties as HV's own connection, which is none. See
# seed-bri-contract.sh for grant properties.
default_grant_properties='{}'
GRANT_PROPERTIES="${GRANT_PROPERTIES:-$default_grant_properties}"
if ! jq -e 'type == "object"' >/dev/null 2>&1 <<<"$GRANT_PROPERTIES"; then
  echo "GRANT_PROPERTIES must be a JSON object, got: $GRANT_PROPERTIES" >&2
  exit 1
fi

GRANT_LINK_PATH="${GRANT_LINK_PATH:-/bri}"
OUTWAY_NAME="${OUTWAY_NAME:-IntOutway-01}"
PG_HOST="${PG_HOST:-postgres}"
PG_USER="${PG_USER:-postgres}"
export PGPASSWORD="${PGPASSWORD:-${FSC_POSTGRES_PASSWORD}}"

for f in "$INT_CERT" "$INT_KEY" "$INT_CA" "$INT_ORG_CERT" "$HV_CERT" "$HV_KEY" "$HV_CA"; do
  if [ ! -f "$f" ]; then
    echo "missing cert file: $f" >&2
    echo "run 'make fsc-int-certs fsc-hv-certs' first" >&2
    exit 1
  fi
done

# The delegated connection contracts at the integrator's Manager for this
# service, delegator and grant properties. As in seed-bri-contract.sh, the
# Manager ignores ?service_name=, so the match is client-side.
delegated_contracts() {
  mtls_curl "$INT_CERT" "$INT_KEY" "$INT_CA" \
    "$INT_MANAGER_URL/v1/contracts?grant_type=GRANT_TYPE_DELEGATED_SERVICE_CONNECTION" \
    | jq -c --arg svc "$SERVICE_NAME" --arg bd "$BD_PEER_ID" --arg hv "$HV_PEER_ID" --argjson properties "$GRANT_PROPERTIES" \
      '[.contracts[]?
        | select(.content.grants[0].service.name == $svc)
        | select(.content.grants[0].service.peer_id == $bd)
        | select(.content.grants[0].delegator.peer_id == $hv)
        | select((.content.grants[0].properties // {}) == $properties)]'
}

# ── 1. Delegated connection contract at the integrator's Manager ──────

echo "[1/3] Create delegated connection INT → BD for '$SERVICE_NAME' on behalf of HV..."

if delegated_contracts | jq -e 'any(.[]; .state == "CONTRACT_STATE_VALID")' >/dev/null; then
  echo "  → delegated connection contract already Valid, skipping"
else
  outway_thumbprint=$(pubkey_thumbprint_hex "$INT_ORG_CERT")
  if [ -z "$outway_thumbprint" ]; then
    echo "  ✗ could not compute outway pubkey thumbprint"
    exit 1
  fi
  body=$(jq -n \
    --arg iv "$(uuid_v7)" \
    --arg group "$GROUP_ID" \
    --argjson not_before "$(now_epoch)" \
    --argjson not_after "$(plus_years_epoch 5)" \
    --argjson created_at "$(now_epoch)" \
    --arg svc_name "$SERVICE_NAME" \
    --arg bd_peer "$BD_PEER_ID" \
    --arg hv_peer "$HV_PEER_ID" \
    --arg int_peer "$INT_PEER_ID" \
    --arg thumb "$outway_thumbprint" \
    --argjson properties "$GRANT_PROPERTIES" \
    '{
      contract_content: {
        iv: $iv,
        group_id: $group,
        validity: {not_before: $not_before, not_after: $not_after},
        hash_algorithm: "HASH_ALGORITHM_SHA3_512",
        created_at: $created_at,
        grants: [{
          type: "GRANT_TYPE_DELEGATED_SERVICE_CONNECTION",
          delegator: {peer_id: $hv_peer},
          outway: {
            peer_id: $int_peer,
            identification: {
              type: "OUTWAY_IDENTIFICATION_TYPE_PUBLIC_KEY_THUMBPRINT",
              public_key_thumbprint: $thumb
            }
          },
          service: {type: "SERVICE_TYPE_SERVICE", peer_id: $bd_peer, name: $svc_name},
          properties: $properties
        }]
      }
    }')
  http_status=$(mtls_curl "$INT_CERT" "$INT_KEY" "$INT_CA" \
    "$INT_MANAGER_URL/v1/contracts" \
    -X POST -H "Content-Type: application/json" \
    -d "$body" -o /tmp/int-conn-out.txt -w "%{http_code}")
  if [ "$http_status" != "201" ]; then
    echo "  ✗ delegated connection contract creation failed (HTTP $http_status)"
    cat /tmp/int-conn-out.txt
    exit 1
  fi
  content_hash=$(jq -r '.content_hash' /tmp/int-conn-out.txt)
  echo "  ✓ contract created (${content_hash:0:22}...)"

  # ── 2. The delegator signs ──────────────────────────────────────────
  echo "[2/3] Accept as delegator at HV's Manager..."
  accepted=false
  for _ in $(seq 30); do
    status=$(mtls_curl "$HV_CERT" "$HV_KEY" "$HV_CA" \
      "$HV_MANAGER_URL/v1/contracts/$content_hash/accept" \
      -X PUT -o /tmp/hv-accept-out.txt -w "%{http_code}" || echo "000")
    if [ "$status" = "204" ]; then
      accepted=true
      break
    fi
    # The integrator's Manager delivers the contract asynchronously; until it
    # arrives, HV's Manager does not know the hash.
    sleep 1
  done
  if [ "$accepted" != "true" ]; then
    echo "  ✗ HV could not accept the contract (last HTTP $status)"
    cat /tmp/hv-accept-out.txt
    exit 1
  fi
  echo "  ✓ accepted by HV; waiting for BD's autosign..."

  for _ in $(seq 30); do
    sleep 1
    st=$(delegated_contracts | jq -r --arg hash "$content_hash" 'first(.[] | select(.hash == $hash) | .state) // ""')
    if [ "$st" = "CONTRACT_STATE_VALID" ]; then
      echo "  ✓ delegated connection contract Valid"
      break
    fi
  done
  [ "${st:-}" = "CONTRACT_STATE_VALID" ] ||
    fail_unsigned "delegated connection contract for '$SERVICE_NAME'" "${st:-}"
fi

# ── 3. Grant-link upsert in int_controller ────────────────────────────

echo "[3/3] Upsert grant-link '$GRANT_LINK_PATH' → delegated grant-hash..."
new_hash=$(delegated_contracts | jq -r 'first(.[] | select(.state == "CONTRACT_STATE_VALID") | .content.grants[0].hash) // empty')
if [ -z "$new_hash" ]; then
  echo "  x no delegated grant-hash found — abort"
  exit 1
fi
psql -h "$PG_HOST" -U "$PG_USER" -d fsc_int_controller -c "
  INSERT INTO controller.outway_grant_links (outway_name, url_path, grant_hash, outway_group_id)
  VALUES ('$OUTWAY_NAME', '$GRANT_LINK_PATH', '$new_hash', '$GROUP_ID')
  ON CONFLICT (outway_group_id, outway_name, url_path)
  DO UPDATE SET grant_hash = EXCLUDED.grant_hash
" > /dev/null
echo "  ✓ grant-link $OUTWAY_NAME $GRANT_LINK_PATH → ${new_hash:0:22}..."

echo ""
echo "Delegated contract-seed done. The integrator POSTs to"
echo "http://int-outway:8080$GRANT_LINK_PATH on behalf of Hypotheek-BV."
