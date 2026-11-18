#!/usr/bin/env bash
# Generates the certificates of one or more FSC parties from the party table
# fsc-infra/peers.txt.
#
#   bash bootstrap-peer.sh NAME...                  group + internal certs
#   bash bootstrap-peer.sh --internal-only NAME...  internal certs only
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
    echo "Usage: $(basename "$0") [--internal-only] NAME..." >&2
    echo "NAME is a party from fsc-infra/peers.txt." >&2
    exit 2
}

INTERNAL_ONLY=0
if [[ "${1:-}" == "--internal-only" ]]; then
    INTERNAL_ONLY=1
    shift
fi
[[ $# -gt 0 ]] || usage

# Prints the row of party $1 as eight tab-separated fields; fails if the
# party is not in the table or its row does not have eight fields. A row is
# a line whose first field starts with a letter or digit.
peer_row () {
    awk -F'|' -v name="$1" '
        {
            for (i = 1; i <= NF; i++) gsub(/^[[:space:]]+|[[:space:]]+$/, "", $i)
            if ($1 !~ /^[[:alnum:]]/) next  # comment or empty line
            if ($1 != name) next
            if (NF != 8) { printf "ERROR: %s: row %s has %d fields, not 8\n", FILENAME, name, NF > "/dev/stderr"; exit 1 }
            OFS = "\t"; $1 = $1; print; found = 1
        }
        END { exit !found }
    ' "${PEERS_FILE}"
}

# Run inside the pki-tools container, with the fields of one row in the
# environment and fsc-infra mounted on /work.
# shellcheck disable=SC2016 # expanded by the container's bash
CONTAINER_SCRIPT='
set -euo pipefail
cd /work
PKI="/work/${DIR}/pki"

if [[ "${DO_GROUP}" == 1 ]]; then
    echo "-- group-cert: ${NAME} (OIN=${OIN}, hosts=${HOSTS}) --"
    OUT=$(mktemp -d)

    # openssl requires an adjusted config to declare serialNumber as a DN
    # attribute.
    echo -e "[req_distinguished_name]\nserialNumber=OIN" >> /etc/ssl/openssl.cnf

    SAN_LIST=""
    for H in ${HOSTS//,/ }; do
        SAN_LIST="${SAN_LIST}DNS:${H},"
    done

    openssl req -new -nodes -sha256 -newkey rsa:4096 \
        -subj "/C=NL/O=${ORG}/OU=TEST/CN=${HOSTS%%,*}/serialNumber=${OIN}" \
        -addext "subjectAltName=${SAN_LIST%,}" \
        -keyout "${OUT}/${NAME}-key.pem" \
        -out "${OUT}/${NAME}.csr" 2>/dev/null

    CSR_JSON=$(jq -sR . < "${OUT}/${NAME}.csr")
    curl -fsS -X POST http://certportal:8080/api/request_certificate \
        -H "Content-Type: application/json" \
        -d "{\"csr\":${CSR_JSON}}" \
    | jq -r ".certificate" > "${OUT}/${NAME}.pem"

    if [[ ! -s "${OUT}/${NAME}.pem" ]]; then
        echo "FAIL: no cert for ${NAME}" >&2
        exit 1
    fi

    # Sanity: subject log + chain check
    openssl x509 -in "${OUT}/${NAME}.pem" -noout -subject
    openssl verify -CAfile pki/ca/root.pem -untrusted pki/ca/intermediate.pem "${OUT}/${NAME}.pem" >/dev/null

    mkdir -p "${PKI}/org"
    mv "${OUT}/${NAME}-key.pem" "${PKI}/org/"
    mv "${OUT}/${NAME}.pem"     "${PKI}/org/"
    cp pki/ca/root.pem          "${PKI}/org/"
    chmod 600 "${PKI}/org/${NAME}-key.pem"
fi

if [[ "${INTERNAL_CA}" != "-" ]]; then
    echo "-- internal-CA + internal-cert for ${NAME} --"
    TMP=$(mktemp -d)
    cd "${TMP}"
    jq -n --arg cn "GBO-DEMO Internal CA (${INTERNAL_CA})" --arg o "${INTERNAL_ORG}" \
        "{CN: \$cn, key: {algo: \"rsa\", size: 4096}, names: [{C: \"NL\", O: \$o}], ca: {expiry: \"43800h\"}}" \
        > internal-ca.json
    jq -n --arg cn "${NAME}-internal" --arg o "${INTERNAL_ORG}" --arg hosts "${INTERNAL_HOSTS}" \
        "{CN: \$cn, hosts: (\$hosts | split(\",\")), key: {algo: \"rsa\", size: 4096}, names: [{C: \"NL\", O: \$o}]}" \
        > internal-cert.json
    cfssl gencert -initca internal-ca.json | cfssljson -bare intermediate_ca
    cfssl gencert \
        -ca=intermediate_ca.pem \
        -ca-key=intermediate_ca-key.pem \
        -config=/work/pki/cfssl-signing-config.json \
        -profile=server \
        internal-cert.json \
    | cfssljson -bare internal-cert

    mkdir -p "${PKI}/internal"
    mv intermediate_ca.pem      "${PKI}/internal/"
    mv intermediate_ca-key.pem  "${PKI}/internal/"
    mv internal-cert.pem        "${PKI}/internal/"
    mv internal-cert-key.pem    "${PKI}/internal/"
    chmod 600 "${PKI}/internal/"*-key.pem
fi

# OpenFSC containers run as appuser (uid/gid 1001).
if [[ "${DO_GROUP}" == 1 ]]; then
    chown -R 1001:1001 "${PKI}"
elif [[ "${INTERNAL_CA}" != "-" ]]; then
    chown -R 1001:1001 "${PKI}/internal"
fi
'

if [[ "${INTERNAL_ONLY}" == 0 && ( ! -f ca/root.pem || ! -f ca/intermediate.pem ) ]]; then
    echo "ERROR: root-CA not found. Run first: bash generate-root-ca.sh"
    exit 1
fi

for PEER in "$@"; do
    if ! ROW=$(peer_row "${PEER}"); then
        echo "ERROR: no valid row for party '${PEER}' in fsc-infra/peers.txt" >&2
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
        bash -c "${CONTAINER_SCRIPT}"

    if [[ "${INTERNAL_ONLY}" == 0 ]]; then
        echo
        echo ">>> Done."
        ls -la "../${DIR}/pki/org/"
        if [[ "${INTERNAL_CA}" != "-" ]]; then ls -la "../${DIR}/pki/internal/"; fi
    fi
done
