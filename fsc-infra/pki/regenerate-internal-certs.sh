#!/usr/bin/env bash
# One-off script: regenerates ALL internal-certs (every party in
# fsc-infra/peers.txt that has them) without touching the group certs.
#
# Use after changing internal_org or internal_hosts in peers.txt (e.g. new
# SANs). Group-cert thumbprints stay identical -> existing contracts stay
# valid.

set -o errexit
set -o pipefail
set -o nounset

cd "$(dirname "$0")"

# Every party, in table order; bootstrap-peer.sh skips those without
# internal certs.
NAMES=$(bash bootstrap-peer.sh --list | cut -f1)
# shellcheck disable=SC2086 # one party name per word
bash bootstrap-peer.sh --internal-only ${NAMES}

echo
echo ">>> Done. Restart all FSC containers and dev-portal-backend to load the new internal certs."
