#!/usr/bin/env bash
# Generates cert-tuples for the directory-peer + directory-UI.
#
# Requires:
#   - Root-CA + intermediate-CA (pki/ca/*.pem) — via generate-root-ca.sh
#   - Certportal running on the fsc-infra_default network (make fsc-up)
#
# Cert generation uses **openssl** (not cfssl genkey) because CFSSL's
# JSON config ignores `serialNumber` in the `names` section. OpenSSL
# handles it — requires an adjusted openssl.cnf that declares
# serialNumber as a DN field. Same pattern as try-me's
# init-organization-certs.sh.
#
# Idempotent: skipped if certs already exist.

set -o errexit
set -o pipefail
set -o nounset

cd "$(dirname "$0")"

IMAGE_TAG="gbo-demo/pki-tools:local"
NETWORK="fsc-infra_default"

DIRPEER_ORG="../directory-peer/pki/org"
DIRPEER_INT="../directory-peer/pki/internal"
DIRUI_ORG="../directory-ui/pki/org"

if [[ ! -f ca/root.pem || ! -f ca/intermediate.pem ]]; then
    echo "ERROR: root-CA not found. Run first: bash generate-root-ca.sh"
    exit 1
fi

if [[ -f "${DIRPEER_ORG}/directory-peer.pem" && -f "${DIRUI_ORG}/directory-ui.pem" && -f "${DIRPEER_INT}/internal-cert.pem" ]]; then
    echo "Directory-peer certs already exist. Remove them to regenerate."
    exit 0
fi

echo ">>> Generating directory-peer + directory-UI cert-tuples"

# ── Group certs via openssl + certportal ────────────────────────────────
_request_group_cert () {
    local NAME="$1" ORG="$2" OIN="$3" HOSTS="$4" DEST="$5"

    echo "-- group-cert: ${NAME} (OIN=${OIN}, hosts=${HOSTS}) --"

    docker run --rm \
        --network "${NETWORK}" \
        -v "$(pwd)/..:/work" -w /work \
        -e NAME="${NAME}" \
        -e ORG="${ORG}" \
        -e OIN="${OIN}" \
        -e HOSTS="${HOSTS}" \
        -e DEST="${DEST}" \
        "${IMAGE_TAG}" \
        bash -c '
            set -euo pipefail
            OUT=$(mktemp -d)

            # openssl requires an adjusted config to declare
            # serialNumber as a DN attribute.
            echo -e "[req_distinguished_name]\nserialNumber=OIN" >> /etc/ssl/openssl.cnf

            PRIMARY_HOST="${HOSTS%%,*}"

            SAN_LIST=""
            for H in $(echo "$HOSTS" | tr "," " "); do
                SAN_LIST="${SAN_LIST}DNS:${H},"
            done
            SAN_LIST="${SAN_LIST%,}"

            openssl req -new -nodes -sha256 -newkey rsa:4096 \
                -subj "/C=NL/O=${ORG}/OU=TEST/CN=${PRIMARY_HOST}/serialNumber=${OIN}" \
                -addext "subjectAltName=${SAN_LIST}" \
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

            # Sanity: chain check + subject log
            openssl x509 -in "${OUT}/${NAME}.pem" -noout -subject
            openssl verify -CAfile pki/ca/root.pem -untrusted pki/ca/intermediate.pem "${OUT}/${NAME}.pem" >/dev/null

            mkdir -p "/work/${DEST}"
            mv "${OUT}/${NAME}-key.pem" "/work/${DEST}/${NAME}-key.pem"
            mv "${OUT}/${NAME}.pem"     "/work/${DEST}/${NAME}.pem"
            cp pki/ca/root.pem          "/work/${DEST}/root.pem"
        '
}

# Directory-peer group-cert
_request_group_cert \
    "directory-peer" \
    "GBO-DEMO Directory Peer" \
    "99999999900000000000" \
    "directory-manager,directory-inway" \
    "directory-peer/pki/org"

# Directory-UI group-cert
_request_group_cert \
    "directory-ui" \
    "GBO-DEMO Directory UI" \
    "99999999900000000010" \
    "directory-ui" \
    "directory-ui/pki/org"

# ── Internal certs (self-signed intermediate, for intra-org) ──────────
echo "-- internal-CA + internal-cert for directory-peer --"
docker run --rm \
    -v "$(pwd)/..:/work" -w /work \
    "${IMAGE_TAG}" \
    bash -c '
        set -euo pipefail
        cd pki
        mkdir -p /tmp/int
        cd /tmp/int
        cfssl gencert -initca /work/pki/internal-ca.json | cfssljson -bare intermediate_ca
        cfssl gencert \
            -ca=intermediate_ca.pem \
            -ca-key=intermediate_ca-key.pem \
            -config=/work/pki/cfssl-signing-config.json \
            -profile=server \
            /work/pki/directory-internal-cert.json \
        | cfssljson -bare internal-cert

        mkdir -p /work/directory-peer/pki/internal
        mv intermediate_ca.pem      /work/directory-peer/pki/internal/
        mv intermediate_ca-key.pem  /work/directory-peer/pki/internal/
        mv internal-cert.pem        /work/directory-peer/pki/internal/
        mv internal-cert-key.pem    /work/directory-peer/pki/internal/
        chmod 600 /work/directory-peer/pki/internal/*-key.pem
    '

# Key files land root-owned via the docker runs; restrict + hand over to
# appuser (uid/gid 1001) which the OpenFSC containers run as.
docker run --rm -v "$(pwd)/..:/work" "${IMAGE_TAG}" \
    bash -c '
        chmod 600 /work/directory-peer/pki/org/*-key.pem \
                  /work/directory-ui/pki/org/*-key.pem
        chown -R 1001:1001 /work/directory-peer/pki /work/directory-ui/pki
    '

echo
echo ">>> Done."
ls -la ../directory-peer/pki/org/ ../directory-peer/pki/internal/ ../directory-ui/pki/org/
