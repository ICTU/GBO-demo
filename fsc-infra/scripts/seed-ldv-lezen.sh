#!/usr/bin/env bash
# seed-ldv-lezen — publish the read API of each source's delivery logbook
# over FSC and connect the consent register to it (#686).
#
# Each source peer publishes the read listener (4018) of the logbook its
# sidecar records deliveries in as a service behind its own Inway, and the
# register's peer (...0700) connects to it; a grant link on cr-outway lets
# the register address it by name:
#
#   ldv-lezen-bd   ...0200  bd-inway   logboek-levering-bd:4018
#   ldv-lezen-lvg  ...0200  bd-inway   logboek-levering-lvg:4018
#   ldv-lezen-brp  ...0400  brp-inway  logboek-levering-brp:4018
#
# The source Managers sign the connection only for a peer the onboarding
# register admits to them; ...0700 is listed for ...0200 and ...0400. The
# Inway then puts every read to the source's PDP (LDV_READ_FSC).
#
# Parameterised and idempotent like seed-bri-contract.sh, which does the
# work. Set SERVICES to seed a subset.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FSC_INFRA_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

SERVICES="${SERVICES:-ldv-lezen-bd ldv-lezen-lvg ldv-lezen-brp}"

# service | endpoint | Inway | provider peer | Controller | Manager | internal certs dir
provider() {
  case "$1" in
    ldv-lezen-bd) echo "http://logboek-levering-bd:4018|https://bd-inway:443|99999999900000000200|https://bd-controller:9444|https://bd-manager:9443|orgs/belastingdienst-mock/pki/internal" ;;
    ldv-lezen-lvg) echo "http://logboek-levering-lvg:4018|https://bd-inway:443|99999999900000000200|https://bd-controller:9444|https://bd-manager:9443|orgs/belastingdienst-mock/pki/internal" ;;
    ldv-lezen-brp) echo "http://logboek-levering-brp:4018|https://brp-inway:443|99999999900000000400|https://brp-controller:9444|https://brp-manager:9443|orgs/brp-mock/pki/internal" ;;
    *) echo "unknown service: $1" >&2; return 1 ;;
  esac
}

for service in $SERVICES; do
  IFS='|' read -r endpoint inway peer controller manager internal_dir <<<"$(provider "$service")"
  echo "── $service on $peer ──"
  SERVICE_NAME="$service" \
  SERVICE_ENDPOINT_URL="$endpoint" \
  SERVICE_INWAY_ADDRESS="$inway" \
  PROVIDER_PEER_ID="$peer" \
  PROVIDER_CONTROLLER_URL="$controller" \
  PROVIDER_MANAGER_URL="$manager" \
  PROVIDER_INTERNAL_DIR="$FSC_INFRA_DIR/$internal_dir" \
  CONSUMER_PEER_ID=99999999900000000700 \
  CONSUMER_MANAGER_URL=https://cr-manager:9443 \
  CONSUMER_INTERNAL_DIR="$FSC_INFRA_DIR/orgs/consent-register/pki/internal" \
  CONSUMER_ORG_CERT="$FSC_INFRA_DIR/orgs/consent-register/pki/org/consent-register.pem" \
  CONSUMER_CONTROLLER_DB=fsc_cr_controller \
  GRANT_LINK_PATH="/$service" \
  OUTWAY_NAME=CrOutway-01 \
    bash "$SCRIPT_DIR/seed-bri-contract.sh"
done
