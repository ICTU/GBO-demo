#!/usr/bin/env bash
# Before seeding consent-authorization: the consent register's Manager signs
# a connection only for a peer the onboarding register admits to the consent
# register's peer (...0700). The demo configuration admits both demo
# providers, but the register seeds its participants into a new volume only;
# an existing volume keeps the participants of its first start. Without this
# check the seed waits 30 s for a signature that never comes.

set -euo pipefail

register_peer="99999999900000000700"
providers=("99999999900000000300" "99999999900000001000")
admin_port="${GBO_PORT_DVTP_ONBOARDING_REGISTER:-9415}"

if ! command -v jq >/dev/null 2>&1; then
  echo "ERROR: jq is not installed; it is needed to read the DvTP onboarding register (see README.md)" >&2
  exit 1
fi

if ! feed=$(docker compose exec -T dvtp-onboarding-register \
  wget -qO- http://localhost:4015/internal/openftv/participants 2>/dev/null); then
  echo "ERROR: cannot read the DvTP onboarding register; is it running? (make pdp-up starts it)" >&2
  exit 1
fi

if ! jq -e 'type == "array"' >/dev/null 2>&1 <<<"$feed"; then
  echo "ERROR: the DvTP onboarding register's participant list is not a JSON list:" >&2
  head -c 300 <<<"$feed" >&2
  echo >&2
  exit 1
fi

missing=()
for peer in "${providers[@]}"; do
  if ! jq -e --arg peer "$peer" --arg source "$register_peer" \
    'any(.[]; .peer_id == $peer and ((.allowed_source_peer_ids // []) | index($source) != null))' \
    >/dev/null <<<"$feed"; then
    missing+=("$peer")
  fi
done

if [ "${#missing[@]}" -gt 0 ]; then
  {
    echo "ERROR: the DvTP onboarding register does not admit ${missing[*]} to the"
    echo "consent register ($register_peer), so its Manager will not sign their"
    echo "connection to consent-authorization. The register volume predates that"
    echo "admission: the demo seeds reach a new volume only. Either"
    echo "  - add $register_peer to the sources of each of these participants at"
    echo "    http://localhost:$admin_port and wait 5 s for the OpenFTV PIP pull, or"
    echo "  - reset the register with the rest of the demo data: make demo-down,"
    echo "    then docker compose down -v,"
    echo "then run make demo again. See TROUBLESHOOTING.md."
  } >&2
  exit 1
fi
echo "  ✓ the onboarding register admits the demo providers to the consent register"
