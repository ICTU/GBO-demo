#!/usr/bin/env bash
# Generates cert-tuples for the Belastingdienst-mock (provider).
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
NETWORK="fsc-infra_default"

BD_ORG="../orgs/belastingdienst-mock/pki/org"
BD_INT="../orgs/belastingdienst-mock/pki/internal"

if [[ ! -f ca/root.pem || ! -f ca/intermediate.pem ]]; then
    echo "ERROR: root-CA not found. Run first: bash generate-root-ca.sh"
    exit 1
fi

if [[ -f "${BD_ORG}/bd-mock.pem" && -f "${BD_INT}/internal-cert.pem" ]]; then
    echo "Belastingdienst-mock certs already exist. Remove them to regenerate."
    exit 0
fi

echo ">>> Generating Belastingdienst-mock cert-tuple"

# ── Group cert via openssl + certportal ────────────────────────────────
echo "-- group-cert: bd-mock (OIN=99999999900000000200, hosts=bd-manager,bd-inway) --"
docker run --rm \
    --network "${NETWORK}" \
    -v "$(pwd)/..:/work" -w /work \
    "${IMAGE_TAG}" \
    bash -c '
        set -euo pipefail
        OUT=$(mktemp -d)

        echo -e "[req_distinguished_name]\nserialNumber=OIN" >> /etc/ssl/openssl.cnf

        openssl req -new -nodes -sha256 -newkey rsa:4096 \
            -subj "/C=NL/O=GBO-DEMO Belastingdienst-mock/OU=TEST/CN=bd-manager/serialNumber=99999999900000000200" \
            -addext "subjectAltName=DNS:bd-manager,DNS:bd-inway" \
            -keyout "${OUT}/bd-mock-key.pem" \
            -out "${OUT}/bd-mock.csr" 2>/dev/null

        CSR_JSON=$(jq -sR . < "${OUT}/bd-mock.csr")
        curl -fsS -X POST http://certportal:8080/api/request_certificate \
            -H "Content-Type: application/json" \
            -d "{\"csr\":${CSR_JSON}}" \
        | jq -r ".certificate" > "${OUT}/bd-mock.pem"

        if [[ ! -s "${OUT}/bd-mock.pem" ]]; then
            echo "FAIL: no cert for bd-mock" >&2
            exit 1
        fi

        openssl x509 -in "${OUT}/bd-mock.pem" -noout -subject
        openssl verify -CAfile pki/ca/root.pem -untrusted pki/ca/intermediate.pem "${OUT}/bd-mock.pem" >/dev/null

        mkdir -p /work/orgs/belastingdienst-mock/pki/org
        mv "${OUT}/bd-mock-key.pem" /work/orgs/belastingdienst-mock/pki/org/
        mv "${OUT}/bd-mock.pem"     /work/orgs/belastingdienst-mock/pki/org/
        cp pki/ca/root.pem           /work/orgs/belastingdienst-mock/pki/org/
        chmod 600 /work/orgs/belastingdienst-mock/pki/org/bd-mock-key.pem
    '

# ── Internal certs (self-signed intermediate, for intra-org) ──────────
echo "-- internal-CA + internal-cert for bd-mock --"
docker run --rm \
    -v "$(pwd)/..:/work" -w /work \
    "${IMAGE_TAG}" \
    bash -c '
        set -euo pipefail
        mkdir -p /tmp/int
        cd /tmp/int
        cfssl gencert -initca /work/pki/bd-mock-internal-ca.json | cfssljson -bare intermediate_ca
        cfssl gencert \
            -ca=intermediate_ca.pem \
            -ca-key=intermediate_ca-key.pem \
            -config=/work/pki/cfssl-signing-config.json \
            -profile=server \
            /work/pki/bd-mock-internal-cert.json \
        | cfssljson -bare internal-cert

        mkdir -p /work/orgs/belastingdienst-mock/pki/internal
        mv intermediate_ca.pem      /work/orgs/belastingdienst-mock/pki/internal/
        mv intermediate_ca-key.pem  /work/orgs/belastingdienst-mock/pki/internal/
        mv internal-cert.pem        /work/orgs/belastingdienst-mock/pki/internal/
        mv internal-cert-key.pem    /work/orgs/belastingdienst-mock/pki/internal/
        chmod 600 /work/orgs/belastingdienst-mock/pki/internal/*-key.pem
    '

echo
echo ">>> Done."
ls -la ../orgs/belastingdienst-mock/pki/org/ ../orgs/belastingdienst-mock/pki/internal/
