#!/usr/bin/env bash
# seed-bri-contract — reproduce a service contract between the
# edi-issuer consumer and a configurable source provider.
#
# Parameterised by provider, service, endpoint and grant-property settings so
# the same script seeds every source peer and every service it offers. The
# demo has:
#   bri -> bron-sidecar     -> graphql-server     (BD bronprofiel)
#   brp -> brp-sidecar      -> brp-graphql-server (BRP bronprofiel)
# See the Makefile targets fsc-seed-bri / fsc-seed-brp.
#
# Background:
#   The flow was originally set up manually via the Controller UIs.
#   Contract state survives container restarts but not `make fsc-clean`.
#   This script restores it.
#
# Requires the counterparties to accept the contracts. The directory-peer
# auto-signs publications by grant type; the source Managers put every
# incoming contract to the OpenFTV PDP (policies/fsc/autosign.rego), so a
# private consumer that is not active for that source in the DvTP onboarding
# register is refused and the contract stays `proposed`. Both cases abort the
# seed — see fail_unsigned.
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
PROVIDER_PEER_ID="${PROVIDER_PEER_ID:-99999999900000000200}"
DIR_PEER_ID="${DIR_PEER_ID:-99999999900000000000}"   # Directory-peer

# Endpoints — in-network hostnames. Script runs in fsc-infra_default via
# `docker run` (the Makefile target arranges this). Manager and
# Controller accept mTLS with each org's internal-cert set.
PROVIDER_CONTROLLER_URL="${PROVIDER_CONTROLLER_URL:-https://bd-controller:9444}"
PROVIDER_MANAGER_URL="${PROVIDER_MANAGER_URL:-https://bd-manager:9443}"
EDI_MANAGER_URL="${EDI_MANAGER_URL:-https://edi-manager:9443}"          # manager int

# Service endpoint — the bron-sidecar reads the grant property
# subject_id_type and substitutes PI -> BSN if needed; direct mode is
# pass-through to the bron. For the demo always via a sidecar.
SERVICE_ENDPOINT_URL="${SERVICE_ENDPOINT_URL:-http://bron-sidecar:4011}"
SERVICE_INWAY_ADDRESS="${SERVICE_INWAY_ADDRESS:-https://bd-inway:443}"

# Grant properties (fsc-core §Properties) on the service-connection grant.
# They are part of the grant hash, so both peers countersign the regime the
# consumer is judged under, and the provider Manager emits them in the access
# token as the `prp` claim. Two consumers read them from there:
#   flow            — authorization regime; the OpenFTV request-mapper
#                     dispatches on it and denies when it is absent
#   subject_id_type — 'pseudonym' makes the bron-sidecar substitute PI -> BSN,
#                     'direct' passes the query through unchanged
# Requires OpenFSC >= v2.0.0 on every peer on the contract; the demo pins
# v2.4.0 (fsc-infra/docker-compose.yml).
default_grant_properties='{"flow": "eudi:attestation", "subject_id_type": "direct"}'
GRANT_PROPERTIES="${GRANT_PROPERTIES:-$default_grant_properties}"
if ! jq -e 'type == "object"' >/dev/null 2>&1 <<<"$GRANT_PROPERTIES"; then
  echo "GRANT_PROPERTIES must be a JSON object, got: $GRANT_PROPERTIES" >&2
  exit 1
fi

# Grant-link config (edi-Controller side). v2.4.0 has no REST endpoint
# for grant-link CRUD, so we upsert directly into the Controller DB
# after contract-seed. Path must be a single segment (Outway matches
# parts[0]).
GRANT_LINK_PATH="${GRANT_LINK_PATH:-/bri}"
CREATE_GRANT_LINK="${CREATE_GRANT_LINK:-true}"
OUTWAY_NAME="${OUTWAY_NAME:-EdiOutway-01}"
PG_HOST="${PG_HOST:-postgres}"
PG_USER="${PG_USER:-postgres}"
export PGPASSWORD="${PGPASSWORD:-${FSC_POSTGRES_PASSWORD}}"

# mTLS credentials (internal-cert per org, mounted under fsc-infra/orgs/*).
PROVIDER_INTERNAL_DIR="${PROVIDER_INTERNAL_DIR:-$FSC_INFRA_DIR/orgs/belastingdienst-mock/pki/internal}"
EDI_INTERNAL_DIR="$FSC_INFRA_DIR/orgs/edi-issuer/pki/internal"

PROVIDER_CERT="$PROVIDER_INTERNAL_DIR/internal-cert.pem"
PROVIDER_KEY="$PROVIDER_INTERNAL_DIR/internal-cert-key.pem"
PROVIDER_CA="$PROVIDER_INTERNAL_DIR/intermediate_ca.pem"

EDI_CERT="$EDI_INTERNAL_DIR/internal-cert.pem"
EDI_KEY="$EDI_INTERNAL_DIR/internal-cert-key.pem"
EDI_CA="$EDI_INTERNAL_DIR/intermediate_ca.pem"

# Peer/org certs (needed for pubkey-thumbprint of the outway).
EDI_ORG_CERT="$FSC_INFRA_DIR/orgs/edi-issuer/pki/org/edi-issuer.pem"

for f in "$PROVIDER_CERT" "$PROVIDER_KEY" "$PROVIDER_CA" "$EDI_CERT" "$EDI_KEY" "$EDI_CA" "$EDI_ORG_CERT"; do
  if [ ! -f "$f" ]; then
    echo "missing cert file: $f" >&2
    echo "provision the EDI and selected provider FSC certificates first" >&2
    exit 1
  fi
done

# ── 1. Register service in provider Controller ─────────────────────────────

echo "[1/4] Register service '$SERVICE_NAME' in provider Controller..."

existing_services=$(mtls_curl "$PROVIDER_CERT" "$PROVIDER_KEY" "$PROVIDER_CA" \
  "$PROVIDER_CONTROLLER_URL/v1/services" || echo '{}')

if echo "$existing_services" | jq -e --arg n "$SERVICE_NAME" \
   '.services[]? | select(.name == $n)' >/dev/null 2>&1; then
  echo "  → service '$SERVICE_NAME' already registered, skipping"
else
  create_body=$(jq -n \
    --arg name "$SERVICE_NAME" \
    --arg endpoint "$SERVICE_ENDPOINT_URL" \
    --arg inway "$SERVICE_INWAY_ADDRESS" \
    '{name: $name, endpoint_url: $endpoint, inway_address: $inway}')
  http_status=$(mtls_curl "$PROVIDER_CERT" "$PROVIDER_KEY" "$PROVIDER_CA" \
    "$PROVIDER_CONTROLLER_URL/v1/services" \
    -X POST -H "Content-Type: application/json" \
    -d "$create_body" -o /tmp/seed-svc-out.txt -w "%{http_code}")
  if [ "$http_status" != "201" ]; then
    echo "  ✗ service creation failed (HTTP $http_status)"
    cat /tmp/seed-svc-out.txt
    exit 1
  fi
  echo "  ✓ service created"
fi

# ── 2. Create publication contract in provider Manager ─────────────────────

echo "[2/4] Create publication contract for '$SERVICE_NAME' at provider Manager..."

existing_pub=$(mtls_curl "$PROVIDER_CERT" "$PROVIDER_KEY" "$PROVIDER_CA" \
  "$PROVIDER_MANAGER_URL/v1/contracts?grant_type=GRANT_TYPE_SERVICE_PUBLICATION&service_name=$SERVICE_NAME" \
  || echo '{}')

# NOTE: the Manager's ?service_name= filter is NOT honoured (v2.4.0
# returns every contract regardless), so the service name has to be
# matched client-side. Without this a second service silently reuses the
# first service's contracts and grant-hash.
if echo "$existing_pub" | jq -e --arg svc "$SERVICE_NAME" --arg provider "$PROVIDER_PEER_ID" \
   '.contracts[]? | select(.state == "CONTRACT_STATE_VALID") | select(.content.grants[0].service.name == $svc and .content.grants[0].service.peer_id == $provider)' >/dev/null 2>&1; then
  echo "  → publication contract already Valid, skipping"
else
  pub_body=$(jq -n \
    --arg iv "$(uuid_v7)" \
    --arg group "$GROUP_ID" \
    --argjson not_before "$(now_epoch)" \
    --argjson not_after "$(plus_years_epoch 5)" \
    --argjson created_at "$(now_epoch)" \
    --arg svc_name "$SERVICE_NAME" \
    --arg provider_peer "$PROVIDER_PEER_ID" \
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
          service: {peer_id: $provider_peer, name: $svc_name, protocol: "PROTOCOL_TCP_HTTP_1.1"}
        }]
      }
    }')
  http_status=$(mtls_curl "$PROVIDER_CERT" "$PROVIDER_KEY" "$PROVIDER_CA" \
    "$PROVIDER_MANAGER_URL/v1/contracts" \
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
    st=$(mtls_curl "$PROVIDER_CERT" "$PROVIDER_KEY" "$PROVIDER_CA" \
      "$PROVIDER_MANAGER_URL/v1/contracts?grant_type=GRANT_TYPE_SERVICE_PUBLICATION&service_name=$SERVICE_NAME" \
      | jq -r --arg svc "$SERVICE_NAME" --arg provider "$PROVIDER_PEER_ID" \
        'first(.contracts[]? | select(.content.grants[0].service.name == $svc and .content.grants[0].service.peer_id == $provider) | .state)' 2>/dev/null || echo "")
    if [ "$st" = "CONTRACT_STATE_VALID" ]; then
      echo "  ✓ publication contract Valid"
      break
    fi
  done
  [ "${st:-}" = "CONTRACT_STATE_VALID" ] ||
    fail_unsigned "publication contract for '$SERVICE_NAME'" "${st:-}"
fi

# ── 3. Create connection contract in edi-Manager ───────────────────────────

echo "[3/4] Create connection contract for '$SERVICE_NAME' at edi-Manager..."

existing_conn=$(mtls_curl "$EDI_CERT" "$EDI_KEY" "$EDI_CA" \
  "$EDI_MANAGER_URL/v1/contracts?grant_type=GRANT_TYPE_SERVICE_CONNECTION&service_name=$SERVICE_NAME" \
  || echo '{}')

# Contracts are immutable, so a changed regime means a NEW contract (new iv),
# not an update. Matching on the properties as well as on the service
# therefore does double duty: it keeps the seed idempotent, and it re-seeds by
# itself when the desired properties changed — as they did when flow and
# subject_id_type moved out of the additional-claims mapping and back into the
# grant. The superseded contract stays Valid but unused: step 4 repoints the
# grant-link at the new grant hash. `make fsc-clean` removes the old one.
if echo "$existing_conn" | jq -e --arg svc "$SERVICE_NAME" --arg provider "$PROVIDER_PEER_ID" --argjson properties "$GRANT_PROPERTIES" \
   '.contracts[]? | select(.state == "CONTRACT_STATE_VALID") | select(.content.grants[0].service.name == $svc and .content.grants[0].service.peer_id == $provider) | select((.content.grants[0].properties // {}) == $properties)' >/dev/null 2>&1; then
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
    --arg provider_peer "$PROVIDER_PEER_ID" \
    --arg edi_peer "$EDI_PEER_ID" \
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
          type: "GRANT_TYPE_SERVICE_CONNECTION",
          outway: {
            peer_id: $edi_peer,
            identification: {
              type: "OUTWAY_IDENTIFICATION_TYPE_PUBLIC_KEY_THUMBPRINT",
              public_key_thumbprint: $thumb
            }
          },
          service: {type: "SERVICE_TYPE_SERVICE", peer_id: $provider_peer, name: $svc_name},
          properties: $properties
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
    st=$(printf '%s' "$contracts_json" | jq -r --arg svc "$SERVICE_NAME" --arg provider "$PROVIDER_PEER_ID" --argjson properties "$GRANT_PROPERTIES" \
      'first(.contracts[]? | select(.content.grants[0].service.name == $svc and .content.grants[0].service.peer_id == $provider) | select((.content.grants[0].properties // {}) == $properties) | .state) // ""' 2>/dev/null)
    if [ "$st" = "CONTRACT_STATE_VALID" ]; then
      echo "  ✓ connection contract Valid"
      break
    fi
  done
  [ "${st:-}" = "CONTRACT_STATE_VALID" ] ||
    fail_unsigned "connection contract for '$SERVICE_NAME'" "${st:-}"
fi

# ── 4. Grant-link upsert in edi_controller ────────────────────────────
# v2.4.0 has no REST endpoint for grant-links (only Controller-UI web
# form). Direct SQL is the shortest path — the shape is stable and the
# table is read frequently by edi-outway.

echo "[4/4] Upsert grant-link '$GRANT_LINK_PATH' → connection grant-hash..."
if [ "$CREATE_GRANT_LINK" != "true" ]; then
  echo "  → skipped; callers send Fsc-Grant-Hash explicitly"
  echo
  echo "Contract-seed done."
  exit 0
fi
# No limit= here. As noted at step 2 the Manager ignores ?service_name=,
# so the service has to be matched client-side — and limit=1 truncates
# the response to whichever contract happens to come first, which is
# another service's as soon as the provider offers more than one.
# Selecting on state as well, so a revoked contract cannot supply the hash
# and write a dead grant-link that fails at routing time instead of here.
# Selecting on the properties too, so a contract superseded by a property
# change cannot keep the grant-link pointing at the previous regime.
new_hash=$(mtls_curl "$EDI_CERT" "$EDI_KEY" "$EDI_CA" \
  "${EDI_MANAGER_URL}/v1/contracts?grant_type=GRANT_TYPE_SERVICE_CONNECTION&service_name=${SERVICE_NAME}" \
  | jq -r --arg svc "$SERVICE_NAME" --arg provider "$PROVIDER_PEER_ID" --argjson properties "$GRANT_PROPERTIES" \
    'first(.contracts[]?
           | select(.state == "CONTRACT_STATE_VALID")
           | select(.content.grants[0].service.name == $svc)
           | select(.content.grants[0].service.peer_id == $provider)
           | select((.content.grants[0].properties // {}) == $properties)
           | .content.grants[0].hash) // empty')
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
echo "Contract-seed done. Adapter can POST to $GRANT_LINK_PATH (via outway) —"
echo "outway resolves it to the grant-hash + mTLS to the provider Inway."
