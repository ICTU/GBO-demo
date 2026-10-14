#!/usr/bin/env bash
# Generates cert-tuples for the ASIP (services/asi-provider): the Authentic
# Source Interface Provider of the ETSI TS 119 478 route, which queries a
# source over FSC as its own consumer peer.
#
# Analogous to bootstrap-edi-issuer.sh / bootstrap-bd-mock.sh:
#   - Group cert via openssl + our certportal (subject.serialNumber = OIN)
#   - Internal-CA + internal-cert self-signed via cfssl
#
# Requires: root-CA (make fsc-ca) + certportal running (make fsc-up).
# Idempotent: skipped if certs already exist.

set -o errexit
set -o pipefail
set -o nounset

cd "$(dirname "$0")"

IMAGE_TAG="gbo-demo/pki-tools:local"
NETWORK="${FSC_INFRA_NETWORK:-fsc-infra_default}"

ORG_DIR="../orgs/asip/pki/org"
INT_DIR="../orgs/asip/pki/internal"

if [[ ! -f ca/root.pem || ! -f ca/intermediate.pem ]]; then
    echo "ERROR: root-CA not found. Run first: bash generate-root-ca.sh"
    exit 1
fi

if [[ -f "${ORG_DIR}/asip.pem" && -f "${INT_DIR}/internal-cert.pem" ]]; then
    echo "ASIP certs already exist. Remove them to regenerate."
    exit 0
fi

echo ">>> Generating ASIP cert-tuple"

# ── Group cert via openssl + certportal ────────────────────────────────
echo "-- group-cert: asip (OIN=99999999900000001200, hosts=asip-manager,asip-outway) --"
docker run --rm \
    --network "${NETWORK}" \
    -v "$(pwd)/..:/work" -w /work \
    "${IMAGE_TAG}" \
    bash -c '
        set -euo pipefail
        OUT=$(mktemp -d)

        echo -e "[req_distinguished_name]\nserialNumber=OIN" >> /etc/ssl/openssl.cnf

        openssl req -new -nodes -sha256 -newkey rsa:4096 \
            -subj "/C=NL/O=GBO-DEMO ASIP/OU=TEST/CN=asip-manager/serialNumber=99999999900000001200" \
            -addext "subjectAltName=DNS:asip-manager,DNS:asip-outway" \
            -keyout "${OUT}/asip-key.pem" \
            -out "${OUT}/asip.csr" 2>/dev/null

        CSR_JSON=$(jq -sR . < "${OUT}/asip.csr")
        curl -fsS -X POST http://certportal:8080/api/request_certificate \
            -H "Content-Type: application/json" \
            -d "{\"csr\":${CSR_JSON}}" \
        | jq -r ".certificate" > "${OUT}/asip.pem"

        if [[ ! -s "${OUT}/asip.pem" ]]; then
            echo "FAIL: no cert for asip" >&2
            exit 1
        fi

        openssl x509 -in "${OUT}/asip.pem" -noout -subject
        openssl verify -CAfile pki/ca/root.pem -untrusted pki/ca/intermediate.pem "${OUT}/asip.pem" >/dev/null

        mkdir -p /work/orgs/asip/pki/org
        mv "${OUT}/asip-key.pem" /work/orgs/asip/pki/org/
        mv "${OUT}/asip.pem"     /work/orgs/asip/pki/org/
        cp pki/ca/root.pem                    /work/orgs/asip/pki/org/
        chmod 600 /work/orgs/asip/pki/org/asip-key.pem
    '

# ── Internal certs (self-signed intermediate, for intra-org) ──────────
echo "-- internal-CA + internal-cert for asip --"
docker run --rm \
    -v "$(pwd)/..:/work" -w /work \
    "${IMAGE_TAG}" \
    bash -c '
        set -euo pipefail
        mkdir -p /tmp/int
        cd /tmp/int
        cfssl gencert -initca /work/pki/asip-internal-ca.json | cfssljson -bare intermediate_ca
        cfssl gencert \
            -ca=intermediate_ca.pem \
            -ca-key=intermediate_ca-key.pem \
            -config=/work/pki/cfssl-signing-config.json \
            -profile=server \
            /work/pki/asip-internal-cert.json \
        | cfssljson -bare internal-cert

        mkdir -p /work/orgs/asip/pki/internal
        mv intermediate_ca.pem      /work/orgs/asip/pki/internal/
        mv intermediate_ca-key.pem  /work/orgs/asip/pki/internal/
        mv internal-cert.pem        /work/orgs/asip/pki/internal/
        mv internal-cert-key.pem    /work/orgs/asip/pki/internal/
        chmod 600 /work/orgs/asip/pki/internal/*-key.pem
    '

# OpenFSC containers run as appuser (uid/gid 1001) and must be able to
# read the key material.
docker run --rm -v "$(pwd)/..:/work" "${IMAGE_TAG}" \
    chown -R 1001:1001 /work/orgs/asip/pki

echo
echo ">>> Done."
ls -la ../orgs/asip/pki/org/ ../orgs/asip/pki/internal/
