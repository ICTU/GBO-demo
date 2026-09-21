#!/usr/bin/env bash
# Generates cert-tuples for the shared PDP's own FSC peer (#383).
# The PDP asks the consent register for a consent's status through it.
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

ORG_DIR="../orgs/gbo-pdp/pki/org"
INT_DIR="../orgs/gbo-pdp/pki/internal"

if [[ ! -f ca/root.pem || ! -f ca/intermediate.pem ]]; then
    echo "ERROR: root-CA not found. Run first: bash generate-root-ca.sh"
    exit 1
fi

if [[ -f "${ORG_DIR}/gbo-pdp.pem" && -f "${INT_DIR}/internal-cert.pem" ]]; then
    echo "GBO-PDP certs already exist. Remove them to regenerate."
    exit 0
fi

echo ">>> Generating GBO-PDP cert-tuple"

# ── Group cert via openssl + certportal ────────────────────────────────
echo "-- group-cert: gbo-pdp (OIN=99999999900000000800, hosts=pdp-manager,pdp-outway) --"
docker run --rm \
    --network "${NETWORK}" \
    -v "$(pwd)/..:/work" -w /work \
    "${IMAGE_TAG}" \
    bash -c '
        set -euo pipefail
        OUT=$(mktemp -d)

        echo -e "[req_distinguished_name]\nserialNumber=OIN" >> /etc/ssl/openssl.cnf

        openssl req -new -nodes -sha256 -newkey rsa:4096 \
            -subj "/C=NL/O=GBO-DEMO PDP/OU=TEST/CN=pdp-manager/serialNumber=99999999900000000800" \
            -addext "subjectAltName=DNS:pdp-manager,DNS:pdp-outway" \
            -keyout "${OUT}/gbo-pdp-key.pem" \
            -out "${OUT}/gbo-pdp.csr" 2>/dev/null

        CSR_JSON=$(jq -sR . < "${OUT}/gbo-pdp.csr")
        curl -fsS -X POST http://certportal:8080/api/request_certificate \
            -H "Content-Type: application/json" \
            -d "{\"csr\":${CSR_JSON}}" \
        | jq -r ".certificate" > "${OUT}/gbo-pdp.pem"

        if [[ ! -s "${OUT}/gbo-pdp.pem" ]]; then
            echo "FAIL: no cert for gbo-pdp" >&2
            exit 1
        fi

        openssl x509 -in "${OUT}/gbo-pdp.pem" -noout -subject
        openssl verify -CAfile pki/ca/root.pem -untrusted pki/ca/intermediate.pem "${OUT}/gbo-pdp.pem" >/dev/null

        mkdir -p /work/orgs/gbo-pdp/pki/org
        mv "${OUT}/gbo-pdp-key.pem" /work/orgs/gbo-pdp/pki/org/
        mv "${OUT}/gbo-pdp.pem"     /work/orgs/gbo-pdp/pki/org/
        cp pki/ca/root.pem                    /work/orgs/gbo-pdp/pki/org/
        chmod 600 /work/orgs/gbo-pdp/pki/org/gbo-pdp-key.pem
    '

# ── Internal certs (self-signed intermediate, for intra-org) ──────────
echo "-- internal-CA + internal-cert for gbo-pdp --"
docker run --rm \
    -v "$(pwd)/..:/work" -w /work \
    "${IMAGE_TAG}" \
    bash -c '
        set -euo pipefail
        mkdir -p /tmp/int
        cd /tmp/int
        cfssl gencert -initca /work/pki/gbo-pdp-internal-ca.json | cfssljson -bare intermediate_ca
        cfssl gencert \
            -ca=intermediate_ca.pem \
            -ca-key=intermediate_ca-key.pem \
            -config=/work/pki/cfssl-signing-config.json \
            -profile=server \
            /work/pki/gbo-pdp-internal-cert.json \
        | cfssljson -bare internal-cert

        mkdir -p /work/orgs/gbo-pdp/pki/internal
        mv intermediate_ca.pem      /work/orgs/gbo-pdp/pki/internal/
        mv intermediate_ca-key.pem  /work/orgs/gbo-pdp/pki/internal/
        mv internal-cert.pem        /work/orgs/gbo-pdp/pki/internal/
        mv internal-cert-key.pem    /work/orgs/gbo-pdp/pki/internal/
        chmod 600 /work/orgs/gbo-pdp/pki/internal/*-key.pem
    '

# OpenFSC containers run as appuser (uid/gid 1001) and must be able to
# read the key material.
docker run --rm -v "$(pwd)/..:/work" "${IMAGE_TAG}" \
    chown -R 1001:1001 /work/orgs/gbo-pdp/pki

echo
echo ">>> Done."
ls -la ../orgs/gbo-pdp/pki/org/ ../orgs/gbo-pdp/pki/internal/
