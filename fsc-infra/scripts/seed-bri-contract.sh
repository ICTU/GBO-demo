#!/usr/bin/env bash
# seed-bri-contract — reproduce the bri-service + contract negotiation
# between edi-issuer-org (consumer) and belastingdienst-mock (provider).
#
# Background:
#   The flow was originally set up manually via the Controller UIs.
#   Contract state survives container restarts but not `make fsc-clean`.
#   This script restores it.
#
# Requires auto-sign flags in fsc-infra/docker-compose.yml
# (directory-manager + bd-manager). Without those, contracts stay
# `pending` and someone has to accept them manually.
#
# Idempotent: each step detects existing state and skips.
#
# Grant-links (needed for path-based routing from the outway) are
# handled at the end of this script — v2.4.0 has no REST endpoint for
# grant-links, so we upsert directly into the Controller DB.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FSC_INFRA_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

# ── Configuration ───────────────────────────────────────────────────────────

GROUP_ID="${GROUP_ID:-fsc-demo}"
SERVICE_NAME="${SERVICE_NAME:-bri}"

EDI_PEER_ID="${EDI_PEER_ID:-99999999900000000100}"   # EDI-issuer-org (consumer)
BD_PEER_ID="${BD_PEER_ID:-99999999900000000200}"     # Belastingdienst-mock (provider)
DIR_PEER_ID="${DIR_PEER_ID:-99999999900000000000}"   # Directory-peer

# Endpoints — in-network hostnames. Script runs in fsc-infra_default via
# `docker run` (the Makefile target arranges this). Manager and
# Controller accept mTLS with each org's internal-cert set.
BD_CONTROLLER_URL="${BD_CONTROLLER_URL:-https://bd-controller:9444}"    # controller admin (LISTEN_ADDRESS_ADMINISTRATION_API)
BD_MANAGER_URL="${BD_MANAGER_URL:-https://bd-manager:9443}"             # manager int (LISTEN_ADDRESS_INTERNAL)
EDI_MANAGER_URL="${EDI_MANAGER_URL:-https://edi-manager:9443}"          # manager int

# bri-service endpoint — bron-sidecar reads grant-property
# subject_id_type and substitutes PI -> BSN if needed; direct mode is
# pass-through to graphql-server. For the demo always via the sidecar.
SERVICE_ENDPOINT_URL="${SERVICE_ENDPOINT_URL:-http://bron-sidecar:4011}"
SERVICE_INWAY_ADDRESS="${SERVICE_INWAY_ADDRESS:-https://bd-inway:443}"

# Grant-link config (edi-Controller side). v2.4.0 has no REST endpoint
# for grant-link CRUD, so we upsert directly into the Controller DB
# after contract-seed. Path must be a single segment (Outway matches
# parts[0]).
GRANT_LINK_PATH="${GRANT_LINK_PATH:-/bri}"
OUTWAY_NAME="${OUTWAY_NAME:-EdiOutway-01}"
PG_HOST="${PG_HOST:-postgres}"
PG_USER="${PG_USER:-postgres}"
export PGPASSWORD="${PGPASSWORD:-${FSC_POSTGRES_PASSWORD:-postgres}}"

# mTLS credentials (internal-cert per org, mounted under fsc-infra/orgs/*).
BD_INTERNAL_DIR="$FSC_INFRA_DIR/orgs/belastingdienst-mock/pki/internal"
EDI_INTERNAL_DIR="$FSC_INFRA_DIR/orgs/edi-issuer/pki/internal"

BD_CERT="$BD_INTERNAL_DIR/internal-cert.pem"
BD_KEY="$BD_INTERNAL_DIR/internal-cert-key.pem"
BD_CA="$BD_INTERNAL_DIR/intermediate_ca.pem"

EDI_CERT="$EDI_INTERNAL_DIR/internal-cert.pem"
EDI_KEY="$EDI_INTERNAL_DIR/internal-cert-key.pem"
EDI_CA="$EDI_INTERNAL_DIR/intermediate_ca.pem"

# Peer/org certs (needed for pubkey-thumbprint of the outway).
EDI_ORG_CERT="$FSC_INFRA_DIR/orgs/edi-issuer/pki/org/edi-issuer.pem"

for f in "$BD_CERT" "$BD_KEY" "$BD_CA" "$EDI_CERT" "$EDI_KEY" "$EDI_CA" "$EDI_ORG_CERT"; do
  if [ ! -f "$f" ]; then
    echo "missing cert file: $f" >&2
    echo "run 'make fsc-edi-certs fsc-bd-certs' first" >&2
    exit 1
  fi
done

# ── 1. Register bri-service in bd-Controller ───────────────────────────────

echo "[1/3] Register service '$SERVICE_NAME' in bd-Controller..."

existing_services=$(mtls_curl "$BD_CERT" "$BD_KEY" "$BD_CA" \
  "$BD_CONTROLLER_URL/v1/services" || echo '{}')

if echo "$existing_services" | jq -e --arg n "$SERVICE_NAME" \
   '.services[]? | select(.name == $n)' >/dev/null 2>&1; then
  echo "  → service '$SERVICE_NAME' already registered, skipping"
else
  create_body=$(jq -n \
    --arg name "$SERVICE_NAME" \
    --arg endpoint "$SERVICE_ENDPOINT_URL" \
    --arg inway "$SERVICE_INWAY_ADDRESS" \
    '{name: $name, endpoint_url: $endpoint, inway_address: $inway}')
  http_status=$(mtls_curl "$BD_CERT" "$BD_KEY" "$BD_CA" \
    "$BD_CONTROLLER_URL/v1/services" \
    -X POST -H "Content-Type: application/json" \
    -d "$create_body" -o /tmp/seed-svc-out.txt -w "%{http_code}")
  if [ "$http_status" != "201" ]; then
    echo "  ✗ service creation failed (HTTP $http_status)"
    cat /tmp/seed-svc-out.txt
    exit 1
  fi
  echo "  ✓ service created"
fi

# ── 2. Create publication contract in bd-Manager ───────────────────────────

echo "[2/3] Create publication contract for '$SERVICE_NAME' at bd-Manager..."

existing_pub=$(mtls_curl "$BD_CERT" "$BD_KEY" "$BD_CA" \
  "$BD_MANAGER_URL/v1/contracts?grant_type=GRANT_TYPE_SERVICE_PUBLICATION&service_name=$SERVICE_NAME" \
  || echo '{}')

if echo "$existing_pub" | jq -e '.contracts[]? | select(.state == "CONTRACT_STATE_VALID")' >/dev/null 2>&1; then
  echo "  → publication contract already Valid, skipping"
else
  pub_body=$(jq -n \
    --arg iv "$(uuid_v7)" \
    --arg group "$GROUP_ID" \
    --argjson not_before "$(now_epoch)" \
    --argjson not_after "$(plus_years_epoch 5)" \
    --argjson created_at "$(now_epoch)" \
    --arg svc_name "$SERVICE_NAME" \
    --arg bd_peer "$BD_PEER_ID" \
    --arg dir_peer "$DIR_PEER_ID" \
    '{
      contract_content: {
        iv: $iv,
        group_id: $group,
        validity: {not_before: $not_before, not_after: $not_after},
        hash_algorithm: "HASH_ALGORITHM_SHA3_512",
        created_at: $created_at,
        grants: [{
          type: "GRANT_TYPE_SERVICE_PUBLICATION",
          directory: {peer_id: $dir_peer},
          service: {peer_id: $bd_peer, name: $svc_name, protocol: "PROTOCOL_TCP_HTTP_1.1"},
          properties: {
            "flow": "eudi:attestation",
            "subject_id_type": "direct"
          }
        }]
      }
    }')
  http_status=$(mtls_curl "$BD_CERT" "$BD_KEY" "$BD_CA" \
    "$BD_MANAGER_URL/v1/contracts" \
    -X POST -H "Content-Type: application/json" \
    -d "$pub_body" -o /tmp/seed-pub-out.txt -w "%{http_code}")
  if [ "$http_status" != "201" ]; then
    echo "  ✗ publication contract creation failed (HTTP $http_status)"
    cat /tmp/seed-pub-out.txt
    exit 1
  fi
  echo "  ✓ publication contract created; waiting for auto-sign..."
  # Auto-sign is asynchronous (directory-manager polls); poll up to 30s.
  for _ in $(seq 30); do
    sleep 1
    st=$(mtls_curl "$BD_CERT" "$BD_KEY" "$BD_CA" \
      "$BD_MANAGER_URL/v1/contracts?grant_type=GRANT_TYPE_SERVICE_PUBLICATION&service_name=$SERVICE_NAME" \
      | jq -r 'first(.contracts[]?.state)' 2>/dev/null || echo "")
    if [ "$st" = "CONTRACT_STATE_VALID" ]; then
      echo "  ✓ publication contract Valid"
      break
    fi
  done
fi

# ── 3. Create connection contract in edi-Manager ───────────────────────────

echo "[3/3] Create connection contract for '$SERVICE_NAME' at edi-Manager..."

existing_conn=$(mtls_curl "$EDI_CERT" "$EDI_KEY" "$EDI_CA" \
  "$EDI_MANAGER_URL/v1/contracts?grant_type=GRANT_TYPE_SERVICE_CONNECTION&service_name=$SERVICE_NAME" \
  || echo '{}')

if echo "$existing_conn" | jq -e '.contracts[]? | select(.state == "CONTRACT_STATE_VALID")' >/dev/null 2>&1; then
  echo "  → connection contract already Valid, skipping"
else
  # Outway identification: SHA-256 hex of the peer public key (see openapi.yaml
  # publicKeyThumbprint schema). Outway shares the org-cert as its identity.
  outway_thumbprint=$(pubkey_thumbprint_hex "$EDI_ORG_CERT")
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
    --arg edi_peer "$EDI_PEER_ID" \
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
            peer_id: $edi_peer,
            identification: {
              type: "OUTWAY_IDENTIFICATION_TYPE_PUBLIC_KEY_THUMBPRINT",
              public_key_thumbprint: $thumb
            }
          },
          service: {type: "SERVICE_TYPE_SERVICE", peer_id: $bd_peer, name: $svc_name},
          properties: {
            "flow": "eudi:attestation",
            "subject_id_type": "direct"
          }
        }]
      }
    }')
  http_status=$(mtls_curl "$EDI_CERT" "$EDI_KEY" "$EDI_CA" \
    "$EDI_MANAGER_URL/v1/contracts" \
    -X POST -H "Content-Type: application/json" \
    -d "$conn_body" -o /tmp/seed-conn-out.txt -w "%{http_code}")
  if [ "$http_status" != "201" ]; then
    echo "  ✗ connection contract creation failed (HTTP $http_status)"
    cat /tmp/seed-conn-out.txt
    exit 1
  fi
  echo "  ✓ connection contract created; waiting for auto-sign..."
  for _ in $(seq 30); do
    sleep 1
    contracts_json=$(mtls_curl "$EDI_CERT" "$EDI_KEY" "$EDI_CA" \
      "$EDI_MANAGER_URL/v1/contracts?grant_type=GRANT_TYPE_SERVICE_CONNECTION&service_name=$SERVICE_NAME")
    st=$(printf '%s' "$contracts_json" | jq -r 'first(.contracts[]?.state // empty) // ""' 2>/dev/null)
    if [ "$st" = "CONTRACT_STATE_VALID" ]; then
      echo "  ✓ connection contract Valid"
      break
    fi
  done
fi

# ── 4. Grant-link upsert in edi_controller ────────────────────────────
# v2.4.0 has no REST endpoint for grant-links (only Controller-UI web
# form). Direct SQL is the shortest path — the shape is stable and the
# table is read frequently by edi-outway.

echo "[4/4] Upsert grant-link '$GRANT_LINK_PATH' → connection grant-hash..."
new_hash=$(mtls_curl "$EDI_CERT" "$EDI_KEY" "$EDI_CA" \
  "${EDI_MANAGER_URL}/v1/contracts?grant_type=GRANT_TYPE_SERVICE_CONNECTION&service_name=${SERVICE_NAME}&limit=1" \
  | jq -r '.contracts[0].content.grants[0].hash // empty')
if [ -z "$new_hash" ]; then
  echo "  x no connection grant-hash found — abort"
  exit 1
fi
psql -h "$PG_HOST" -U "$PG_USER" -d fsc_edi_controller -c "
  INSERT INTO controller.outway_grant_links (outway_name, url_path, grant_hash, outway_group_id)
  VALUES ('$OUTWAY_NAME', '$GRANT_LINK_PATH', '$new_hash', '$GROUP_ID')
  ON CONFLICT (outway_group_id, outway_name, url_path)
  DO UPDATE SET grant_hash = EXCLUDED.grant_hash
" > /dev/null
echo "  ✓ grant-link $OUTWAY_NAME $GRANT_LINK_PATH → ${new_hash:0:22}..."

echo ""
echo "Contract-seed done. Adapter can POST to /bri (via outway) —"
echo "outway resolves it to the grant-hash + mTLS to bd-inway."
