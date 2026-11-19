#!/usr/bin/env bash
# Generates the certificates of one or more FSC parties from the party table
# fsc-infra/peers.txt.
#
#   bash bootstrap-peer.sh NAME...                  group + internal certs
#   bash bootstrap-peer.sh --internal-only NAME...  internal certs only
#   bash bootstrap-peer.sh --list                   print the table
#
# --list prints every row of the table as eight tab-separated fields, in
# table order, and fails on a row that does not have eight fields. It is the
# one reader of peers.txt: regenerate-internal-certs.sh and the Makefile use
# it too.
#
# Per party, under fsc-infra/<dir>/pki/:
#   org/<name>.pem, org/<name>-key.pem, org/root.pem
#       Group certificate, signed by our certportal (root-CA + intermediate-CA).
#       Made with openssl, not cfssl genkey, because CFSSL's JSON config
#       ignores `serialNumber` in `names`; the subject serialNumber holds the
#       OIN. Same pattern as try-me's init-organization-certs.sh.
#   internal/intermediate_ca.pem, intermediate_ca-key.pem,
#   internal/internal-cert.pem, internal-cert-key.pem
#       Self-signed internal CA and the certificate it signs, for the
#       traffic within the party's own peer (cfssl). Skipped for a party
#       whose internal_ca is "-".
#
# Key files get mode 600 and everything is owned by uid/gid 1001: the OpenFSC
# containers run as appuser and must be able to read the key material.
#
# Without --internal-only it needs the root-CA (make fsc-ca) and a running
# certportal (make fsc-up), and skips a party whose certificates exist; remove
# them to regenerate. --internal-only replaces the internal certificates and
# leaves the group certificates alone, so group-cert thumbprints and the
# contracts signed with them stay valid (see regenerate-internal-certs.sh).

set -o errexit
set -o pipefail
set -o nounset

cd "$(dirname "$0")"

IMAGE_TAG="gbo-demo/pki-tools:local"
NETWORK="${FSC_INFRA_NETWORK:-fsc-infra_default}"
PEERS_FILE="../peers.txt"

usage () {
    echo "Usage: $(basename "$0") [--internal-only] NAME... | --list" >&2
    echo "NAME is a party from fsc-infra/peers.txt." >&2
    exit 2
}

# Prints every row as eight tab-separated fields. A row is a line whose first
# field starts with a letter or digit; other lines are comments or empty.
list_peers () {
    awk -F'|' '
        {
            for (i = 1; i <= NF; i++) gsub(/^[[:space:]]+|[[:space:]]+$/, "", $i)
            if ($1 !~ /^[[:alnum:]]/) next
            if (NF != 8) { printf "ERROR: %s: row %s has %d fields, not 8\n", FILENAME, $1, NF > "/dev/stderr"; exit 1 }
            OFS = "\t"; $1 = $1; print
        }
    ' "${PEERS_FILE}"
}

if [[ "${1:-}" == "--list" ]]; then
    [[ $# -eq 1 ]] || usage
    list_peers
    exit 0
fi

INTERNAL_ONLY=0
if [[ "${1:-}" == "--internal-only" ]]; then
    INTERNAL_ONLY=1
    shift
fi
[[ $# -gt 0 ]] || usage

ROWS=$(list_peers)

# Prints the row of party $1; fails if the party is not in the table.
peer_row () {
    local row
    while IFS= read -r row; do
        if [[ "${row%%$'\t'*}" == "$1" ]]; then
            printf '%s\n' "${row}"
            return 0
        fi
    done <<<"${ROWS}"
    return 1
}

if [[ "${INTERNAL_ONLY}" == 0 && ( ! -f ca/root.pem || ! -f ca/intermediate.pem ) ]]; then
    echo "ERROR: root-CA not found. Run first: bash generate-root-ca.sh"
    exit 1
fi

for PEER in "$@"; do
    if ! ROW=$(peer_row "${PEER}"); then
        echo "ERROR: no party '${PEER}' in fsc-infra/peers.txt" >&2
        exit 1
    fi
    IFS=$'\t' read -r NAME DIR OIN ORG HOSTS INTERNAL_CA INTERNAL_ORG INTERNAL_HOSTS <<<"${ROW}"

    NETWORK_ARGS=()
    if [[ "${INTERNAL_ONLY}" == 1 ]]; then
        if [[ "${INTERNAL_CA}" == "-" ]]; then
            echo "-- ${NAME} has no internal certs, skipped --"
            continue
        fi
        echo "-- internal-cert regen: ${NAME} --"
        DO_GROUP=0
    else
        if [[ -f "../${DIR}/pki/org/${NAME}.pem" \
              && ( "${INTERNAL_CA}" == "-" || -f "../${DIR}/pki/internal/internal-cert.pem" ) ]]; then
            echo "${NAME} certs already exist. Remove them to regenerate."
            continue
        fi
        echo ">>> Generating ${NAME} cert-tuple"
        DO_GROUP=1
        NETWORK_ARGS=(--network "${NETWORK}")
    fi

    docker run --rm \
        ${NETWORK_ARGS[@]+"${NETWORK_ARGS[@]}"} \
        -v "$(pwd)/..:/work" -w /work \
        -e DO_GROUP="${DO_GROUP}" \
        -e NAME="${NAME}" \
        -e DIR="${DIR}" \
        -e OIN="${OIN}" \
        -e ORG="${ORG}" \
        -e HOSTS="${HOSTS}" \
        -e INTERNAL_CA="${INTERNAL_CA}" \
        -e INTERNAL_ORG="${INTERNAL_ORG}" \
        -e INTERNAL_HOSTS="${INTERNAL_HOSTS}" \
        "${IMAGE_TAG}" \
        bash pki/bootstrap-peer-container.sh

    if [[ "${INTERNAL_ONLY}" == 0 ]]; then
        echo
        echo ">>> Done."
        ls -la "../${DIR}/pki/org/"
        if [[ "${INTERNAL_CA}" != "-" ]]; then ls -la "../${DIR}/pki/internal/"; fi
    fi
done
