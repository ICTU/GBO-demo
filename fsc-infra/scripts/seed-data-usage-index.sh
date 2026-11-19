#!/usr/bin/env bash
# seed-data-usage-index — publish the data usage index over FSC and
# connect the source peers to it (#687).
#
# The register's peer (...0700) publishes data-usage-index, the index's
# FSC listener, through cr-inway. Each source peer that delivers under a
# consent connects to it, and a grant link on that peer's Outway lets its
# sidecars address it as /data-usage-index:
#
#   ...0200 (Belastingdienst; also serves LVG)  bd-outway   BdOutway-01
#   ...0400 (RvIG)                              brp-outway  BrpOutway-01
#
# The register's Manager signs a connection only for a peer the onboarding
# register admits to ...0700 (policies/fsc/autosign.rego); the source peers
# are listed there as system participants for that reason.
#
# The same peer publishes data-usage-lookup, the index's lookup listener
# (4020), and connects to it itself through cr-outway: the consent register
# is the only reader, and it reaches the index over FSC like any other
# service, at /data-usage-lookup. Provider and consumer of that contract are
# the same peer (...0700).
#
# Parameterised and idempotent like seed-bri-contract.sh, which does the
# work: the first run registers the service and its publication, every run
# adds the connection of one consumer. Set CONSUMERS to seed a subset, as a
# space-separated list of the names in the table below.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FSC_INFRA_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

SERVICE_NAME="${SERVICE_NAME:-data-usage-index}"
SERVICE_ENDPOINT_URL="${SERVICE_ENDPOINT_URL:-http://data-usage-index:4019}"
GRANT_LINK_PATH="${GRANT_LINK_PATH:-/data-usage-index}"
CONSUMERS="${CONSUMERS:-bd brp}"

# name | peer ID | Manager | internal certs dir | org cert | Controller DB | Outway
consumer() {
  case "$1" in
    bd) echo "99999999900000000200|https://bd-manager:9443|orgs/belastingdienst-mock/pki/internal|orgs/belastingdienst-mock/pki/org/bd-mock.pem|fsc_bd_controller|BdOutway-01" ;;
    brp) echo "99999999900000000400|https://brp-manager:9443|orgs/brp-mock/pki/internal|orgs/brp-mock/pki/org/brp-mock.pem|fsc_brp_controller|BrpOutway-01" ;;
    *) echo "unknown consumer: $1" >&2; return 1 ;;
  esac
}

for name in $CONSUMERS; do
  IFS='|' read -r peer manager internal_dir org_cert controller_db outway <<<"$(consumer "$name")"
  echo "── $SERVICE_NAME for $name ($peer) ──"
  SERVICE_NAME="$SERVICE_NAME" \
  SERVICE_ENDPOINT_URL="$SERVICE_ENDPOINT_URL" \
  SERVICE_INWAY_ADDRESS=https://cr-inway:443 \
  PROVIDER_PEER_ID=99999999900000000700 \
  PROVIDER_CONTROLLER_URL=https://cr-controller:9444 \
  PROVIDER_MANAGER_URL=https://cr-manager:9443 \
  PROVIDER_INTERNAL_DIR="$FSC_INFRA_DIR/orgs/consent-register/pki/internal" \
  CONSUMER_PEER_ID="$peer" \
  CONSUMER_MANAGER_URL="$manager" \
  CONSUMER_INTERNAL_DIR="$FSC_INFRA_DIR/$internal_dir" \
  CONSUMER_ORG_CERT="$FSC_INFRA_DIR/$org_cert" \
  CONSUMER_CONTROLLER_DB="$controller_db" \
  GRANT_LINK_PATH="$GRANT_LINK_PATH" \
  OUTWAY_NAME="$outway" \
    bash "$SCRIPT_DIR/seed-bri-contract.sh"
done

echo "── data-usage-lookup for the consent register (99999999900000000700) ──"
SERVICE_NAME="${LOOKUP_SERVICE_NAME:-data-usage-lookup}" \
SERVICE_ENDPOINT_URL="${LOOKUP_ENDPOINT_URL:-http://data-usage-index:4020}" \
SERVICE_INWAY_ADDRESS=https://cr-inway:443 \
PROVIDER_PEER_ID=99999999900000000700 \
PROVIDER_CONTROLLER_URL=https://cr-controller:9444 \
PROVIDER_MANAGER_URL=https://cr-manager:9443 \
PROVIDER_INTERNAL_DIR="$FSC_INFRA_DIR/orgs/consent-register/pki/internal" \
CONSUMER_PEER_ID=99999999900000000700 \
CONSUMER_MANAGER_URL=https://cr-manager:9443 \
CONSUMER_INTERNAL_DIR="$FSC_INFRA_DIR/orgs/consent-register/pki/internal" \
CONSUMER_ORG_CERT="$FSC_INFRA_DIR/orgs/consent-register/pki/org/consent-register.pem" \
CONSUMER_CONTROLLER_DB=fsc_cr_controller \
GRANT_LINK_PATH=/data-usage-lookup \
OUTWAY_NAME=CrOutway-01 \
  bash "$SCRIPT_DIR/seed-bri-contract.sh"
