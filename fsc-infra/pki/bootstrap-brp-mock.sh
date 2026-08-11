#!/usr/bin/env bash
# Generates cert-tuples for the BRP-mock (provider).
#
# Analogous to bootstrap-edi-issuer.sh:
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

BRP_ORG="../orgs/brp-mock/pki/org"
BRP_INT="../orgs/brp-mock/pki/internal"

if [[ ! -f ca/root.pem || ! -f ca/intermediate.pem ]]; then
    echo "ERROR: root-CA not found. Run first: bash generate-root-ca.sh"
    exit 1
fi

if [[ -f "${BRP_ORG}/brp-mock.pem" && -f "${BRP_INT}/internal-cert.pem" ]]; then
    echo "BRP-mock certs already exist. Remove them to regenerate."
    exit 0
fi

echo ">>> Generating BRP-mock cert-tuple"

# ── Group cert via openssl + certportal ────────────────────────────────
echo "-- group-cert: brp-mock (OIN=99999999900000000400, hosts=brp-manager,brp-inway) --"
docker run --rm \
    --network "${NETWORK}" \
    -v "$(pwd)/..:/work" -w /work \
    "${IMAGE_TAG}" \
    bash -c '
        set -euo pipefail
        OUT=$(mktemp -d)

        echo -e "[req_distinguished_name]\nserialNumber=OIN" >> /etc/ssl/openssl.cnf

        openssl req -new -nodes -sha256 -newkey rsa:4096 \
            -subj "/C=NL/O=GBO-DEMO BRP-mock/OU=TEST/CN=brp-manager/serialNumber=99999999900000000400" \
            -addext "subjectAltName=DNS:brp-manager,DNS:brp-inway" \
            -keyout "${OUT}/brp-mock-key.pem" \
            -out "${OUT}/brp-mock.csr" 2>/dev/null

        CSR_JSON=$(jq -sR . < "${OUT}/brp-mock.csr")
        curl -fsS -X POST http://certportal:8080/api/request_certificate \
            -H "Content-Type: application/json" \
            -d "{\"csr\":${CSR_JSON}}" \
        | jq -r ".certificate" > "${OUT}/brp-mock.pem"

        if [[ ! -s "${OUT}/brp-mock.pem" ]]; then
            echo "FAIL: no cert for brp-mock" >&2
            exit 1
        fi

        openssl x509 -in "${OUT}/brp-mock.pem" -noout -subject
        openssl verify -CAfile pki/ca/root.pem -untrusted pki/ca/intermediate.pem "${OUT}/brp-mock.pem" >/dev/null

        mkdir -p /work/orgs/brp-mock/pki/org
        mv "${OUT}/brp-mock-key.pem" /work/orgs/brp-mock/pki/org/
        mv "${OUT}/brp-mock.pem"     /work/orgs/brp-mock/pki/org/
        cp pki/ca/root.pem           /work/orgs/brp-mock/pki/org/
        chmod 600 /work/orgs/brp-mock/pki/org/brp-mock-key.pem
    '

# ── Internal certs (self-signed intermediate, for intra-org) ──────────
echo "-- internal-CA + internal-cert for brp-mock --"
docker run --rm \
    -v "$(pwd)/..:/work" -w /work \
    "${IMAGE_TAG}" \
    bash -c '
        set -euo pipefail
        mkdir -p /tmp/int
        cd /tmp/int
        cfssl gencert -initca /work/pki/brp-mock-internal-ca.json | cfssljson -bare intermediate_ca
        cfssl gencert \
            -ca=intermediate_ca.pem \
            -ca-key=intermediate_ca-key.pem \
            -config=/work/pki/cfssl-signing-config.json \
            -profile=server \
            /work/pki/brp-mock-internal-cert.json \
        | cfssljson -bare internal-cert

        mkdir -p /work/orgs/brp-mock/pki/internal
        mv intermediate_ca.pem      /work/orgs/brp-mock/pki/internal/
        mv intermediate_ca-key.pem  /work/orgs/brp-mock/pki/internal/
        mv internal-cert.pem        /work/orgs/brp-mock/pki/internal/
        mv internal-cert-key.pem    /work/orgs/brp-mock/pki/internal/
        chmod 600 /work/orgs/brp-mock/pki/internal/*-key.pem
    '

# OpenFSC containers run as appuser (uid/gid 1001) and must be able to
# read the key material.
docker run --rm -v "$(pwd)/..:/work" "${IMAGE_TAG}" \
    chown -R 1001:1001 /work/orgs/brp-mock/pki

echo
echo ">>> Done."
ls -la ../orgs/brp-mock/pki/org/ ../orgs/brp-mock/pki/internal/
