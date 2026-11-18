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

# Every party with internal certs, in table order.
NAMES=()
while IFS= read -r NAME; do NAMES+=("${NAME}"); done < <(awk -F'|' '
    {
        for (i = 1; i <= NF; i++) gsub(/^[[:space:]]+|[[:space:]]+$/, "", $i)
        if ($1 !~ /^[[:alnum:]]/) next  # comment or empty line
        if ($6 != "-") print $1
    }
' ../peers.txt)

bash bootstrap-peer.sh --internal-only "${NAMES[@]}"

echo
echo ">>> Done. Restart all FSC containers and dev-portal-backend to load the new internal certs."
